package postgres_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	if resp.TypeName != "frostmoln_postgres_instance" {
		t.Errorf("expected frostmoln_postgres_instance, got %s", resp.TypeName)
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
		"id", "name", "type", "version", "flavor_id", "storage_gb",
		"vpc_id", "subnet_id", "private_ip", "port", "public_ip", "status", "ha_enabled",
		"ha_status", "backup_enabled", "backup_schedule", "backup_retention_days",
		"parameter_group_id", "security_group_id", "admin_username", "created_at",
		"updated_at", "tenant_id",
		"pitr_enabled", "pitr_capable", "pitr_archive_paused_reason",
		"earliest_restorable_time", "latest_restorable_time",
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
	if typ, ok := s.Attributes["type"].(schema.StringAttribute); !ok || !typ.Computed {
		t.Error("type must be computed")
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &postgresInstanceDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &postgresInstanceDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

func TestIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validInstanceID(id); err == nil {
			t.Errorf("the instance id %q must be refused", id)
		}
	}
	if err := validInstanceID("db-abc123"); err != nil {
		t.Errorf("an ordinary instance id must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       postgresInstanceModel
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
	stringOr := func(v *string) tftypes.Value {
		if v == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *v)
	}
	config := tftypes.NewValue(objType, map[string]tftypes.Value{
		"id":                    stringOr(id),
		"name":                  stringOr(name),
		"type":                  tftypes.NewValue(tftypes.String, nil),
		"version":               tftypes.NewValue(tftypes.String, nil),
		"flavor_id":             tftypes.NewValue(tftypes.String, nil),
		"storage_gb":            tftypes.NewValue(tftypes.Number, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"subnet_id":             tftypes.NewValue(tftypes.String, nil),
		"private_ip":            tftypes.NewValue(tftypes.String, nil),
		"port":                  tftypes.NewValue(tftypes.Number, nil),
		"public_ip":             tftypes.NewValue(tftypes.String, nil),
		"status":                tftypes.NewValue(tftypes.String, nil),
		"ha_enabled":            tftypes.NewValue(tftypes.Bool, nil),
		"ha_status":             tftypes.NewValue(tftypes.String, nil),
		"backup_enabled":        tftypes.NewValue(tftypes.Bool, nil),
		"backup_schedule":       tftypes.NewValue(tftypes.String, nil),
		"backup_retention_days": tftypes.NewValue(tftypes.Number, nil),
		"parameter_group_id":    tftypes.NewValue(tftypes.String, nil),
		"security_group_id":     tftypes.NewValue(tftypes.String, nil),
		"admin_username":        tftypes.NewValue(tftypes.String, nil),
		"created_at":            tftypes.NewValue(tftypes.String, nil),
		"updated_at":            tftypes.NewValue(tftypes.String, nil),
		"tenant_id":             tftypes.NewValue(tftypes.String, nil),

		"pitr_enabled":               tftypes.NewValue(tftypes.Bool, nil),
		"pitr_capable":               tftypes.NewValue(tftypes.Bool, nil),
		"pitr_archive_paused_reason": tftypes.NewValue(tftypes.String, nil),
		"earliest_restorable_time":   tftypes.NewValue(tftypes.String, nil),
		"latest_restorable_time":     tftypes.NewValue(tftypes.String, nil),
	})

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

	id, name := "db-1", "billing"
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
		if r.URL.Path != "/v1/tenants/tenant-1/databases/db-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"db-1","name":"billing","type":"postgresql","typeVersion":"16",
			"flavorId":"db.gp1.small","storageGb":50,"vpcId":"vpc-1","subnetId":"subnet-1",
			"privateIp":"10.0.1.5","port":5432,"status":"running",
			"haEnabled":true,"haStatus":"healthy","backupEnabled":true,
			"backupSchedule":"0 3 * * *","backupRetentionDays":14,
			"securityGroupId":"sg-1","adminUsername":"billingadmin",
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z",
			"tenantId":"tenant-1"
		}`))
	})
	defer server.Close()

	id := "db-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/databases/db-1" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.ID.ValueString() != "db-1" || m.Name.ValueString() != "billing" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.Type.ValueString() != "postgresql" || m.Version.ValueString() != "16" {
		t.Errorf("type/version is %s/%s", m.Type.ValueString(), m.Version.ValueString())
	}
	if m.FlavorID.ValueString() != "db.gp1.small" || m.StorageGB.ValueInt64() != 50 {
		t.Errorf("flavor/storage are %s/%d", m.FlavorID.ValueString(), m.StorageGB.ValueInt64())
	}
	if m.PrivateIP.ValueString() != "10.0.1.5" || m.Port.ValueInt64() != 5432 {
		t.Errorf("address is %s:%d", m.PrivateIP.ValueString(), m.Port.ValueInt64())
	}
	if !m.PublicIP.IsNull() {
		t.Errorf("public_ip must be null when the instance is not public")
	}
	if m.HAEnabled.ValueBool() != true || m.HAStatus.ValueString() != "healthy" {
		t.Errorf("ha is %t/%s", m.HAEnabled.ValueBool(), m.HAStatus.ValueString())
	}
	if m.BackupEnabled.ValueBool() != true || m.BackupSchedule.ValueString() != "0 3 * * *" {
		t.Errorf("backup is %t/%s", m.BackupEnabled.ValueBool(), m.BackupSchedule.ValueString())
	}
	if m.BackupRetentionDays.ValueInt64() != 14 {
		t.Errorf("backup retention is %d", m.BackupRetentionDays.ValueInt64())
	}
	if m.SecurityGroupID.ValueString() != "sg-1" || m.AdminUsername.ValueString() != "billingadmin" {
		t.Errorf("sg/admin are %s/%s", m.SecurityGroupID.ValueString(), m.AdminUsername.ValueString())
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
}

func TestReadByID404ErrorsThatTheInstanceDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	id := "db-gone"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the instance is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// A 200 that does not carry the requested id is not the instance read this
// provider builds its contract on.
func TestReadByIDRefusesA200ThatDoesNotCarryTheID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/databases/db-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"db-other","name":"other","type":"postgresql","status":"running"}`))
	})
	defer server.Close()

	id := "db-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested instance") {
		t.Errorf("the diagnostic must name the identity guard: %s", got)
	}
}

// A mysql instance behind a postgres id (or name) can NEVER satisfy this data
// source — the type check runs on both paths.
func TestReadByIDRefusesAMysqlInstance(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/databases/db-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"db-1","name":"legacy","type":"mysql","typeVersion":"8","status":"running"}`))
	})
	defer server.Close()

	id := "db-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mysql row must never satisfy the postgres data source")
	}
	if got := readResp.text(); !strings.Contains(got, "mysql") || !strings.Contains(got, "frostmoln_mysql_instance") {
		t.Errorf("the diagnostic must name the type and the matching data source: %s", got)
	}
}

// --- the name path ---

func TestReadByNameResolvesFromTheList(t *testing.T) {
	var gotQuery atomic.Pointer[url.Values]
	// A name lookup resolves the id from the LIST and then re-READS the
	// instance by id: the restorable window and the archive pause reason are
	// served on the single-instance GET and on nothing else, so the list row
	// alone would render an instance as having no window at all. The handler
	// therefore answers both shapes.
	newListBody := func(pages [][]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if id := strings.TrimPrefix(r.URL.Path, "/v1/tenants/tenant-1/databases/"); id != r.URL.Path && id != "" {
				for _, page := range pages {
					for _, row := range page {
						if strings.Contains(row, `"id":"`+id+`"`) {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, row)
							return
						}
					}
				}
				writeFlatError(w, http.StatusNotFound, "not_found", "no such instance")
				return
			}
			q := r.URL.Query()
			gotQuery.Store(&q)
			limit, _ := strconv.Atoi(q.Get("limit"))
			if limit == 0 {
				limit = 50
			}
			offset, _ := strconv.Atoi(q.Get("offset"))
			pageIdx := offset / limit
			rows := []string{}
			if pageIdx < len(pages) {
				rows = pages[pageIdx]
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"instances":[%s],"totalCount":%d}`,
				strings.Join(rows, ","), len(rows))
		}
	}

	billing := `{"id":"db-billing","name":"billing","type":"postgresql","typeVersion":"16","flavorId":"db.gp1.small","storageGb":50,"vpcId":"vpc-1","subnetId":"subnet-1","privateIp":"10.0.1.5","port":5432,"status":"running","createdAt":"2026-01-01T00:00:00Z"}`
	other := `{"id":"db-other","name":"analytics","type":"postgresql","typeVersion":"17","flavorId":"db.gp1.small","storageGb":20,"vpcId":"vpc-1","subnetId":"subnet-1","privateIp":"10.0.1.9","port":5432,"status":"running","createdAt":"2026-01-02T00:00:00Z"}`

	server := newTestServer(t, newListBody([][]string{{other, billing}}))
	defer server.Close()

	name := "billing"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || (q.Get("limit") == "" && q.Get("offset") == "") {
		t.Errorf("the list request must page explicitly, got query %v", q)
	}
	m := readResp.model
	if m.ID.ValueString() != "db-billing" || m.Version.ValueString() != "16" {
		t.Errorf("resolution is %s/%s", m.ID.ValueString(), m.Version.ValueString())
	}
	if m.PrivateIP.ValueString() != "10.0.1.5" || m.Port.ValueInt64() != 5432 {
		t.Errorf("address is %s:%d", m.PrivateIP.ValueString(), m.Port.ValueInt64())
	}
	if !m.UpdatedAt.IsNull() {
		t.Errorf("updated_at absent on the wire must stay null")
	}
}

// A mysql row with the right name stays invisible to the postgres resolver:
// absent, not resolved wrong.
func TestReadByNameIgnoresAMysqlInstanceWithTheSameName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instances":[{"id":"db-1","name":"legacy","type":"mysql","typeVersion":"8","status":"running"}],"totalCount":1}`))
	})
	defer server.Close()

	name := "legacy"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mysql row must not resolve")
	}
	if got := readResp.text(); !strings.Contains(got, "frostmoln_mysql_instance") {
		t.Errorf("the diagnostic must point at the matching data source: %s", got)
	}
}

func TestReadByName404WhenNoInstanceMatches(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instances":[{"id":"db-1","name":"analytics","type":"postgresql","status":"running"}],"totalCount":1}`))
	})
	defer server.Close()

	name := "does-not-exist"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No PostgreSQL instance with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// Two postgres instances with the same name: refuse, naming the colliding ids.
func TestReadByNameRefusesAnAmbiguousName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instances":[
			{"id":"db-a","name":"billing","type":"postgresql","status":"running"},
			{"id":"db-b","name":"billing","type":"postgresql","status":"running"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "billing"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous name must refuse")
	}
	got := readResp.text()
	if !strings.Contains(got, "db-a") || !strings.Contains(got, "db-b") {
		t.Errorf("the diagnostic must name the colliding ids: %s", got)
	}
}

// The list pages at the repository's default limit — a name on page 2 must
// still resolve, across as many requests as it takes.
func TestReadByNameWalksListPages(t *testing.T) {
	var requests atomic.Int32

	row := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"name":"instance-%s","type":"postgresql","status":"running"}`, id, strings.TrimPrefix(id, "db-"))
	}

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The re-read by id that follows a name resolution (see the comment in
		// TestReadByNameResolvesFromTheList). It is deliberately NOT counted:
		// this test is about how many LIST pages the walk fetches.
		if id := strings.TrimPrefix(r.URL.Path, "/v1/tenants/tenant-1/databases/"); id != r.URL.Path && id != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, row(id))
			return
		}
		requests.Add(1)
		offset := r.URL.Query().Get("offset")
		var rows []string
		switch offset {
		case "", "0":
			for i := 0; i < 100; i++ {
				rows = append(rows, row(fmt.Sprintf("db-p1-%02d", i)))
			}
		case "100":
			// A short page: the terminal page carries the billing instance
			// plus one filler, and the walk stops here (short page ends it).
			rows = append(rows, row("db-billing"), row("db-p2-0"))
		default:
			rows = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"instances":[%s],"totalCount":%d}`, strings.Join(rows, ","), len(rows))
	})
	defer server.Close()

	name := "instance-billing"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if readResp.model.ID.ValueString() != "db-billing" {
		t.Errorf("resolution is %q", readResp.model.ID.ValueString())
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("expected exactly 2 page requests, got %d", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the instance; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	id := "db-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the instance; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read PostgreSQL Instance") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	id := "db-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// A null optional field renders as null, never "" or 0 — the absent
// backupRetentionDays is the vector.
func TestReadMapsAbsentOptionalsToNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/databases/db-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"db-1","name":"billing","type":"postgresql","typeVersion":"16","flavorId":"db.gp1.small","storageGb":50,"vpcId":"vpc-1","subnetId":"subnet-1","port":5432,"status":"running","backupEnabled":false,"haEnabled":false,"createdAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "db-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	for name, check := range map[string]func() bool{
		"private_ip":            m.PrivateIP.IsNull,
		"public_ip":             m.PublicIP.IsNull,
		"ha_status":             m.HAStatus.IsNull,
		"backup_schedule":       m.BackupSchedule.IsNull,
		"backup_retention_days": m.BackupRetentionDays.IsNull,
		"parameter_group_id":    m.ParameterGroupID.IsNull,
		"security_group_id":     m.SecurityGroupID.IsNull,
		"admin_username":        m.AdminUsername.IsNull,
		"updated_at":            m.UpdatedAt.IsNull,
	} {
		if !check() {
			t.Errorf("%s must render as null when the wire omits it", name)
		}
	}
}

// The search window: when the tenant's list passes the page cap with a FULL
// final page, zero matches must read as "not in the window we searched" —
// never as the plain-absence copy that asks the operator to re-check spelling.
func TestReadByNameReportsTheSearchWindowInsteadOfAbsence(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Always a full page of non-matching rows: the walk ends on the cap,
		// with the server plainly holding more.
		rows := []string{}
		for i := 0; i < 100; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"db-p-%d","name":"other-%d","type":"postgresql","status":"running"}`, i, i))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"instances":[%s],"totalCount":%d}`, strings.Join(rows, ","), 9999)
	})
	defer server.Close()

	name := "billing"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("the walk must end at the cap")
	}
	got := readResp.text()
	if !strings.Contains(got, "searched window") {
		t.Errorf("a capped walk must report the window, not absence: %s", got)
	}
	if strings.Contains(got, "Check the spelling") {
		t.Errorf("the not-found copy must not fire for a capped walk: %s", got)
	}
}

// --- point-in-time recovery ---

// The window and the archive pause reason are served on the SINGLE-INSTANCE
// GET and on nothing else, so a name lookup that rendered the list row it
// matched would report every instance as having no restorable window at all.
// The two lookup paths must answer identically.
func TestReadCarriesThePITRWindowOnBothLookupPaths(t *testing.T) {
	const full = `{"id":"db-billing","name":"billing","type":"postgresql","typeVersion":"16",
		"flavorId":"db.gp1.small","storageGb":50,"vpcId":"vpc-1","subnetId":"subnet-1",
		"privateIp":"10.0.1.5","port":5432,"status":"running","createdAt":"2026-01-01T00:00:00Z",
		"pitrEnabled":true,"pitrCapable":true,"pitrArchivePausedReason":"tenant_cap",
		"earliestRestorableTime":"2026-09-19T08:00:00Z","latestRestorableTime":"2026-09-20T11:59:31Z"}`
	// The LIST row carries the two booleans but never the window — exactly as
	// the platform serves it.
	const listRow = `{"id":"db-billing","name":"billing","type":"postgresql","typeVersion":"16",
		"flavorId":"db.gp1.small","storageGb":50,"vpcId":"vpc-1","subnetId":"subnet-1",
		"privateIp":"10.0.1.5","port":5432,"status":"running","createdAt":"2026-01-01T00:00:00Z",
		"pitrEnabled":true,"pitrCapable":true}`

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/databases/db-billing") {
			_, _ = io.WriteString(w, full)
			return
		}
		_, _ = fmt.Fprintf(w, `{"instances":[%s],"totalCount":1}`, listRow)
	})
	defer server.Close()

	id, name := "db-billing", "billing"
	for _, tc := range []struct {
		label    string
		id, name *string
	}{
		{"by id", &id, nil},
		{"by name", nil, &name},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := readWith(t, server.URL, tc.id, tc.name)
			if got.diagnostics.HasError() {
				t.Fatalf("read: %v", got.diagnostics.Errors())
			}
			m := got.model
			if !m.PITREnabled.ValueBool() || !m.PITRCapable.ValueBool() {
				t.Errorf("pitr_enabled=%v pitr_capable=%v, want both true", m.PITREnabled, m.PITRCapable)
			}
			if got := m.PITRArchivePausedReason.ValueString(); got != "tenant_cap" {
				t.Errorf("pitr_archive_paused_reason = %q", got)
			}
			if got := m.EarliestRestorableTime.ValueString(); got != "2026-09-19T08:00:00Z" {
				t.Errorf("earliest_restorable_time = %q — a name lookup must re-read the instance, not render the list row", got)
			}
			if got := m.LatestRestorableTime.ValueString(); got != "2026-09-20T11:59:31Z" {
				t.Errorf("latest_restorable_time = %q", got)
			}
		})
	}
}

// A database below the P8 release omits the PITR fields entirely; reading them
// back as `false` would assert something no service said.
func TestReadLeavesAbsentPITRFieldsNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"db-old","name":"old","type":"postgresql","typeVersion":"16",
			"flavorId":"db.gp1.small","storageGb":50,"vpcId":"vpc-1","subnetId":"subnet-1",
			"status":"running","createdAt":"2026-01-01T00:00:00Z"}`)
	})
	defer server.Close()

	id := "db-old"
	got := readWith(t, server.URL, &id, nil)
	if got.diagnostics.HasError() {
		t.Fatalf("read: %v", got.diagnostics.Errors())
	}
	if !got.model.PITREnabled.IsNull() || !got.model.PITRCapable.IsNull() {
		t.Errorf("pitr_enabled=%v pitr_capable=%v, want both null when the platform does not report them",
			got.model.PITREnabled, got.model.PITRCapable)
	}
}
