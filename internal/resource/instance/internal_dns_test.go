package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Internal DNS (MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN Phase 2): the
// instance's hostname and fqdn are read back, and a name taken in the VPC is
// explained as such.

func TestFromAPIMapsHostnameAndFQDN(t *testing.T) {
	var m InstanceModel
	var diags diag.Diagnostics
	m.Tags = types.MapNull(types.StringType)
	m.fromAPI(context.Background(), &apiInstance{
		ID: "i-1", Name: "Web 1", Hostname: "web-1", FQDN: "web-1.prod.ck4to7.omkcmwdd.fm.internal",
	}, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if m.Hostname.ValueString() != "web-1" || m.FQDN.ValueString() != "web-1.prod.ck4to7.omkcmwdd.fm.internal" {
		t.Errorf("hostname/fqdn = %q / %q", m.Hostname.ValueString(), m.FQDN.ValueString())
	}

	// Absent on the wire (no name held; VPC without a domain or with VM names
	// off) is null, not "".
	m.fromAPI(context.Background(), &apiInstance{ID: "i-1", Name: "web-1"}, &diags)
	if !m.Hostname.IsNull() || !m.FQDN.IsNull() {
		t.Errorf("absent hostname/fqdn must be null, got %v / %v", m.Hostname, m.FQDN)
	}
}

func TestCreateNameTakenInTheVPCIsExplained(t *testing.T) {
	for _, tc := range []struct {
		code     string
		wantName bool
	}{
		{code: "AlreadyExists", wantName: true},
		{code: "QuotaExceeded", wantName: false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
					meHandler(w, r)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(map[string]any{"operationId": "op-dup", "status": "pending"})
				case r.URL.Path == "/v1/tenants/tenant-456/operations/op-dup":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"operationId": "op-dup", "status": "failed", "errorCode": tc.code,
						"error": "the request was refused",
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			r := orphanInstanceResource(t, server)
			schemaResp := getInstanceSchema(t)
			tfType := schemaResp.Schema.Type().TerraformType(context.Background())
			planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
				"name": tftypes.NewValue(tftypes.String, "web-1"),
			})
			createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(context.Background(), resource.CreateRequest{
				Plan:   tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: planVal},
			}, createResp)

			if !createResp.Diagnostics.HasError() {
				t.Fatal("expected the refused create to error")
			}
			text := orphanInstanceDiagText(createResp.Diagnostics)
			got := strings.Contains(text, "Instance Name Already Used In The VPC")
			if got != tc.wantName {
				t.Errorf("name-taken diagnostic present = %v, want %v:\n%s", got, tc.wantName, text)
			}
			if tc.wantName && !strings.Contains(text, "create_before_destroy") {
				t.Errorf("the name-taken diagnostic must name the create_before_destroy trap:\n%s", text)
			}
			if !createResp.State.Raw.IsNull() {
				t.Error("a refused create must record no state")
			}
		})
	}
}
