package volume

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- schema, metadata, configure ---

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_volume" {
		t.Errorf("expected frostmoln_volume, got %s", resp.TypeName)
	}
}

func listingSchema(t *testing.T) schema.Schema {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func TestSchemaAttributes(t *testing.T) {
	s := listingSchema(t)
	for _, attr := range []string{
		"id", "name", "description", "size", "status", "volume_type",
		"availability_zone", "bootable", "encrypted", "attachments",
		"source_volume_id", "source_snapshot_id", "source_image_id",
		"iops", "throughput", "metadata", "created_at", "updated_at", "tenant_id",
	} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || !id.Optional {
		t.Error("id must be an optional string attribute")
	}
	if name, ok := s.Attributes["name"].(schema.StringAttribute); !ok || !name.Optional {
		t.Error("name must be an optional string attribute")
	}
	if size, ok := s.Attributes["size"].(schema.Int64Attribute); !ok || !size.Computed {
		t.Error("size must be a computed int attribute")
	}
	if bootable, ok := s.Attributes["bootable"].(schema.BoolAttribute); !ok || !bootable.Computed {
		t.Error("bootable must be a computed bool attribute")
	}
	if metadata, ok := s.Attributes["metadata"].(schema.MapAttribute); !ok || !metadata.Computed {
		t.Error("metadata must be a computed map attribute")
	}
	attachments, ok := s.Attributes["attachments"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("attachments must be a ListNestedAttribute, got %T", s.Attributes["attachments"])
	}
	if !attachments.Computed {
		t.Error("attachments must be computed")
	}
	for _, attr := range []string{"id", "volume_id", "instance_id", "device", "attached_at"} {
		row, ok := attachments.NestedObject.Attributes[attr]
		if !ok {
			t.Errorf("row attribute %s missing from the schema", attr)
			continue
		}
		// A row attribute that is not Computed would surface as an unwanted
		// plan-time input; every row of the passthrough is read-only.
		if !row.IsComputed() {
			t.Errorf("row attribute %s must be computed", attr)
		}
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &volumeDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &volumeDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

func TestIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validVolumeID(id); err == nil {
			t.Errorf("the volume id %q must be refused", id)
		}
	}
	if err := validVolumeID("vol-abc123"); err != nil {
		t.Errorf("an ordinary volume id must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       volumeModel
}

func (r readResult) text() string {
	var b strings.Builder
	for _, d := range r.diagnostics {
		b.WriteString(d.Summary())
		b.WriteString(" ")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

// readWith runs one Read against the server with the given id/name values
// (nil = leave the attribute null), returning diagnostics and the state model.
func readWith(t *testing.T, serverURL string, id, name *string) readResult {
	t.Helper()

	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("tenant-1")

	ds := NewDataSource()
	var cfgResp datasource.ConfigureResponse
	ds.(datasource.DataSourceWithConfigure).Configure(
		context.Background(),
		datasource.ConfigureRequest{ProviderData: c},
		&cfgResp,
	)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", cfgResp.Diagnostics.Errors())
	}

	s := listingSchema(t)
	objType := s.Type().TerraformType(context.Background()).(tftypes.Object)
	configVals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for attr, attrType := range objType.AttributeTypes {
		configVals[attr] = tftypes.NewValue(attrType, nil)
	}
	stringOr := func(v *string) tftypes.Value {
		if v == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *v)
	}
	configVals["id"] = stringOr(id)
	configVals["name"] = stringOr(name)
	config := tftypes.NewValue(objType, configVals)

	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: s}
	ds.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: config},
	}, &readResp)

	var result readResult
	result.diagnostics = readResp.Diagnostics
	if !readResp.Diagnostics.HasError() && !readResp.State.Raw.IsNull() {
		if diags := readResp.State.Get(context.Background(), &result.model); diags.HasError() {
			t.Fatalf("state.Get: %v", diags)
		}
	}
	return result
}

// newTestServer mirrors the data-source test idiom: the /v1/me lookup first
// (unused here — the tenant is set directly), then the handler under test.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"})
	})
	mux.HandleFunc("/", handler)
	return httptest.NewServer(mux)
}

func writeFlatError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

//--- the two-cause refusal matrix ---

// NEITHER id nor name: refused without a request.
func TestReadRefusesAnEmptyLookupWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	readResp := readWith(t, server.URL, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("neither id nor name must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// BOTH id and name: refused without a request.
func TestReadRefusesBothIDAndNameWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	id, name := "vol-1", "billing-data"
	readResp := readWith(t, server.URL, &id, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("both id and name must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// An unusable id fails at the request boundary without a request.
func TestReadRefusesAnUnusableIDWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	dot := ".."
	readResp := readWith(t, server.URL, &dot, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a dot-segment id must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// --- the id path ---

func TestReadByID(t *testing.T) {
	var gotPath atomic.Pointer[string]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		gotPath.Store(&p)
		if r.URL.Path != "/v1/tenants/tenant-1/volumes/vol-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"vol-1","name":"billing-data","description":"data disk for billing",
			"tenantId":"tenant-1","size":100,"status":"in-use","volumeType":"ssd",
			"availabilityZone":"falkenberg-1","bootable":false,"encrypted":true,
			"attachments":[
				{"id":"att-1","volumeId":"vol-1","instanceId":"inst-7","device":"/dev/vdb","attachedAt":"2026-03-01T00:00:00Z"}
			],
			"sourceVolumeId":"vol-0","iops":3000,"throughput":250,
			"metadata":{"env":"prod","customer-id":"tenant-1"},
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z"
		}`))
	})
	defer server.Close()

	id := "vol-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/volumes/vol-1" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.ID.ValueString() != "vol-1" || m.Name.ValueString() != "billing-data" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.Description.ValueString() != "data disk for billing" {
		t.Errorf("description is %q", m.Description.ValueString())
	}
	if m.Size.ValueInt64() != 100 {
		t.Errorf("size is %d", m.Size.ValueInt64())
	}
	if m.Status.ValueString() != "in-use" || m.VolumeType.ValueString() != "ssd" {
		t.Errorf("status/type are %s/%s", m.Status.ValueString(), m.VolumeType.ValueString())
	}
	if m.AvailabilityZone.ValueString() != "falkenberg-1" {
		t.Errorf("availability_zone is %q", m.AvailabilityZone.ValueString())
	}
	if m.Bootable.ValueBool() != false || m.Encrypted.ValueBool() != true {
		t.Errorf("bootable/encrypted are %t/%t", m.Bootable.ValueBool(), m.Encrypted.ValueBool())
	}
	if m.SourceVolumeID.ValueString() != "vol-0" {
		t.Errorf("source_volume_id is %q", m.SourceVolumeID.ValueString())
	}
	if m.IOPS.ValueInt64() != 3000 || m.Throughput.ValueInt64() != 250 {
		t.Errorf("performance is %d/%d", m.IOPS.ValueInt64(), m.Throughput.ValueInt64())
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant_id is %q", m.TenantID.ValueString())
	}

	// The attachments pass through verbatim: one row, the consumer's id and
	// the device path exactly as the wire carries them.
	if m.Attachments.IsNull() {
		t.Fatal("attachments must never be null on a successful read")
	}
	var rows []volumeAttachmentRowModel
	if diags := m.Attachments.ElementsAs(context.Background(), &rows, false); diags.HasError() {
		t.Fatalf("ElementsAs: %v", diags)
	}
	if len(rows) != 1 {
		t.Fatalf("attachments has %d rows, want 1", len(rows))
	}
	if rows[0].ID.ValueString() != "att-1" || rows[0].VolumeID.ValueString() != "vol-1" {
		t.Errorf("row identity is %s/%s", rows[0].ID.ValueString(), rows[0].VolumeID.ValueString())
	}
	if rows[0].InstanceID.ValueString() != "inst-7" || rows[0].Device.ValueString() != "/dev/vdb" {
		t.Errorf("consumer is %s/%s", rows[0].InstanceID.ValueString(), rows[0].Device.ValueString())
	}
	if rows[0].AttachedAt.ValueString() != "2026-03-01T00:00:00Z" {
		t.Errorf("attached_at is %q", rows[0].AttachedAt.ValueString())
	}

	// Metadata renders as the map the wire serves.
	var wireMetadata map[string]string
	if diags := m.Metadata.ElementsAs(context.Background(), &wireMetadata, false); diags.HasError() {
		t.Fatalf("metadata ElementsAs: %v", diags)
	}
	if wireMetadata["env"] != "prod" {
		t.Errorf("metadata env is %q", wireMetadata["env"])
	}
}

func TestReadByID404ErrorsThatTheVolumeDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	id := "vol-gone"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the volume is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// A 200 that does not carry the requested id is not the volume read this
// provider builds its contract on.
func TestReadByIDRefusesA200ThatDoesNotCarryTheID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/volumes/vol-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"vol-other","name":"other","status":"available"}`))
	})
	defer server.Close()

	id := "vol-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested volume") {
		t.Errorf("the diagnostic must name the identity guard: %s", got)
	}
}

// --- the name path ---

func TestReadByNameResolvesFromTheList(t *testing.T) {
	var gotQuery atomic.Pointer[url.Values]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/volumes" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		q := r.URL.Query()
		gotQuery.Store(&q)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volumes":[
			{"id":"vol-other","name":"analytics-volume","size":20,"status":"available","volumeType":"ssd","bootable":false,"encrypted":false,"createdAt":"2026-01-02T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"},
			{"id":"vol-billing","name":"billing-data","size":100,"status":"available","volumeType":"nvme","bootable":false,"encrypted":true,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "billing-data"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || q.Get("name") != "billing-data" {
		t.Errorf("the list request must carry the name filter, got query %v", q)
	}
	m := readResp.model
	if m.ID.ValueString() != "vol-billing" || m.VolumeType.ValueString() != "nvme" {
		t.Errorf("resolution is %s/%s", m.ID.ValueString(), m.VolumeType.ValueString())
	}
	if m.Size.ValueInt64() != 100 || !m.Encrypted.ValueBool() {
		t.Errorf("size/encrypted are %d/%t", m.Size.ValueInt64(), m.Encrypted.ValueBool())
	}
	// An attached nothing still renders as an empty list, not null.
	if m.Attachments.IsNull() || len(m.Attachments.Elements()) != 0 {
		t.Errorf("attachments must render as an empty list, got %v", m.Attachments)
	}
}

// A name that matches nothing in the list fails the read — even though the
// server filter is exact, the empty answer is an error, never an empty row.
func TestReadByName404WhenNoVolumeMatches(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "does-not-exist" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volumes":[{"id":"vol-1","name":"analytics-volume","size":20,"status":"available","volumeType":"ssd","bootable":false,"encrypted":false,"createdAt":"2026-01-02T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"}],"totalCount":1}`))
	})
	defer server.Close()

	name := "does-not-exist"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No volume with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// Two same-named rows survive the exact server filter (the mock returns both,
// as a drifted or filterless service could): refuse, naming the colliding ids.
func TestReadByNameRefusesAnAmbiguousName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "billing-data" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volumes":[
			{"id":"vol-a","name":"billing-data","size":100,"status":"available","volumeType":"ssd","bootable":false,"encrypted":false,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"},
			{"id":"vol-b","name":"billing-data","size":100,"status":"available","volumeType":"ssd","bootable":false,"encrypted":false,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "billing-data"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous name must refuse")
	}
	got := readResp.text()
	if !strings.Contains(got, "vol-a") || !strings.Contains(got, "vol-b") {
		t.Errorf("the diagnostic must name the colliding ids: %s", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the volume; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	id := "vol-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the volume; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read Volume") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	id := "vol-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// Absent optionals render as null, never "" or 0 — the omitted description,
// zone, sources, iops/throughput and metadata are the vectors — and an absent
// attachments array still renders as the empty list.
func TestReadMapsAbsentOptionalsToNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/volumes/vol-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"vol-1","name":"billing-data","tenantId":"tenant-1","size":100,"status":"available","volumeType":"ssd","bootable":false,"encrypted":false,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "vol-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	for name, check := range map[string]func() bool{
		"description":        m.Description.IsNull,
		"availability_zone":  m.AvailabilityZone.IsNull,
		"source_volume_id":   m.SourceVolumeID.IsNull,
		"source_snapshot_id": m.SourceSnapshotID.IsNull,
		"source_image_id":    m.SourceImageID.IsNull,
		"iops":               m.IOPS.IsNull,
		"throughput":         m.Throughput.IsNull,
		"metadata":           m.Metadata.IsNull,
	} {
		if !check() {
			t.Errorf("%s must render as null when the wire omits it", name)
		}
	}
	if m.Attachments.IsNull() || len(m.Attachments.Elements()) != 0 {
		t.Error("an absent attachments array must render as an empty list, never null")
	}
}
