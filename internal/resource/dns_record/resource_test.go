package dns_record

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func recordSet(t *testing.T, values ...string) types.Set {
	t.Helper()
	s, diags := types.SetValueFrom(context.Background(), types.StringType, values)
	if diags.HasError() {
		t.Fatalf("build set: %v", diags)
	}
	return s
}

func TestDNSRecordModelFromAPI(t *testing.T) {
	rec := &apiDNSRecord{
		ID:      "rec-1",
		ZoneID:  "zone-1",
		Name:    "www",
		Type:    "A",
		Records: []string{"203.0.113.10", "203.0.113.11"},
		TTL:     300,
	}

	var model DNSRecordModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), rec, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}

	if model.ID.ValueString() != "rec-1" {
		t.Errorf("expected ID rec-1, got %s", model.ID.ValueString())
	}
	var vals []string
	model.Records.ElementsAs(context.Background(), &vals, false)
	if len(vals) != 2 || vals[1] != "203.0.113.11" {
		t.Errorf("expected two values, got %v", vals)
	}
	// comment was empty -> null, not "".
	if !model.Comment.IsNull() {
		t.Errorf("expected null comment, got %q", model.Comment.ValueString())
	}
}

func TestDNSRecordToUpdateRequestReplacesValues(t *testing.T) {
	model := DNSRecordModel{
		Records: recordSet(t, "203.0.113.20"),
		TTL:     types.Int64Value(600),
		Comment: types.StringNull(),
	}
	var diags diag.Diagnostics
	req := model.toUpdateRequest(context.Background(), &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}

	if len(req.Records) != 1 || req.Records[0] != "203.0.113.20" {
		t.Errorf("expected the value set replaced, got %v", req.Records)
	}
	if req.TTL == nil || *req.TTL != 600 {
		t.Errorf("expected ttl 600, got %v", req.TTL)
	}
}

func TestDNSRecordResourceCRUD(t *testing.T) {
	rec := apiDNSRecord{ID: "rec-1", ZoneID: "zone-1", Name: "www", Type: "A", Records: []string{"203.0.113.10"}, TTL: 300}
	updated := rec
	updated.Records = []string{"203.0.113.20"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1/records":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(rec)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1/records/rec-1":
			_ = json.NewEncoder(w).Encode(rec)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1/records/rec-1":
			_ = json.NewEncoder(w).Encode(updated)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1/records/rec-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	ctx := context.Background()

	if _, err := c.Post(ctx, c.TenantPath("/dns/zones/zone-1/records"), apiCreateDNSRecordRequest{Name: "www", Type: "A", Records: []string{"203.0.113.10"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Get(ctx, c.TenantPath("/dns/zones/zone-1/records/rec-1"), nil); err != nil {
		t.Fatalf("read: %v", err)
	}
	putResp, err := c.Put(ctx, c.TenantPath("/dns/zones/zone-1/records/rec-1"), apiUpdateDNSRecordRequest{Records: []string{"203.0.113.20"}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var got apiDNSRecord
	_ = json.Unmarshal(putResp.Body, &got)
	if got.Records[0] != "203.0.113.20" {
		t.Errorf("expected updated value, got %v", got.Records)
	}
	if _, err := c.Delete(ctx, c.TenantPath("/dns/zones/zone-1/records/rec-1")); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestDNSRecordImportState pins the composite-id import: both halves ride the
// request path (GET/DELETE /dns/zones/{zone}/records/{record}), so a dot
// segment must be refused at the boundary — path.Join would clean a ".."
// into the PARENT ZONE, turning this resource's Delete into a zone delete.
func TestDNSRecordImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	newResp := func() *resource.ImportStateResponse {
		attrs := map[string]tftypes.Value{}
		for name, at := range tfType.(tftypes.Object).AttributeTypes {
			attrs[name] = tftypes.NewValue(at, nil)
		}
		return &resource.ImportStateResponse{
			State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(tfType, attrs)},
		}
	}

	resp := newResp()
	r.ImportState(ctx, resource.ImportStateRequest{ID: "zone-1/rec-2"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var zoneID, id types.String
	resp.State.GetAttribute(ctx, path.Root("zone_id"), &zoneID)
	resp.State.GetAttribute(ctx, path.Root("id"), &id)
	if zoneID.ValueString() != "zone-1" || id.ValueString() != "rec-2" {
		t.Errorf("expected zone-1/rec-2, got %s/%s", zoneID.ValueString(), id.ValueString())
	}

	for _, bad := range []string{"rec-2", "", "zone-1/", "/rec-2", "zone-1/..", "../rec-2", "./rec-2", "../..", "zone-1/rec-2/extra", "zone-1/."} {
		resp := newResp()
		r.ImportState(ctx, resource.ImportStateRequest{ID: bad}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected error for malformed import ID %q", bad)
		}
		if strings.Contains(bad, "..") || strings.HasPrefix(bad, "./") {
			if !importDetailContains(resp.Diagnostics, "collapses") {
				t.Errorf("import ID %q: diagnostic must name the path-collapsing danger, got %v", bad, resp.Diagnostics)
			}
		}
	}
}

// importDetailContains reports whether any diagnostic detail carries want.
func importDetailContains(d diag.Diagnostics, want string) bool {
	for _, e := range d.Errors() {
		if strings.Contains(e.Detail(), want) {
			return true
		}
	}
	return false
}
