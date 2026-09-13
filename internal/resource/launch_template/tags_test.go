package launch_template

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// TestUpdateClearsTags: removing every tag — by deleting the `tags` block or
// by writing `tags = {}` — must reach compute as `"tags": {}`.
//
// compute replaces a template's tags only when the field is non-nil
// (compute/internal/service/impl/launch_template.go, Update: `if req.Tags !=
// nil { template.Tags = req.Tags }`). The update struct carried `omitempty`,
// which drops an empty map, so the clear never left the provider: compute kept
// the old tags, the read-back returned them, and Terraform failed the apply
// with an inconsistent result.
//
// The fake models that constraint rather than answering every write alike: a
// write REPLACES the stored tags only when its body carries a non-null `tags`,
// exactly like compute, so the read-back shows what the real service would.
func TestUpdateClearsTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan types.Map
	}{
		{"tags block removed", types.MapNull(types.StringType)},
		{"tags set to empty map", types.MapValueMust(types.StringType, map[string]attr.Value{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := map[string]string{"a": "b"}
			var patched, hadTags, tagsNull bool
			var sent map[string]string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1":
					patched = true
					raw, _ := io.ReadAll(r.Body)
					var env map[string]json.RawMessage
					if err := json.Unmarshal(raw, &env); err != nil {
						t.Errorf("PATCH body is not a JSON object: %s", raw)
					}
					field, ok := env["tags"]
					hadTags = ok
					tagsNull = ok && string(field) == "null"
					if ok && !tagsNull {
						sent = map[string]string{}
						_ = json.Unmarshal(field, &sent)
						stored = sent
					}
					_ = json.NewEncoder(w).Encode(ltJSON())
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1":
					out := ltJSON()
					out.Tags = stored
					_ = json.NewEncoder(w).Encode(out)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := &launchTemplateResource{client: c}

			stateModel := fullLTModel()
			stateModel.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})
			planModel := fullLTModel()
			planModel.Tags = tc.plan

			state := buildLTState(t, stateModel)
			plan := buildLTPlan(t, planModel)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: configFromPlan(t, plan)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
			}

			if !patched {
				t.Fatal("no PATCH was sent")
			}
			if !hadTags || tagsNull {
				t.Fatalf("PATCH body carried no non-null `tags` (present=%v, null=%v); compute keeps the old tags", hadTags, tagsNull)
			}
			if len(sent) != 0 {
				t.Errorf("PATCH tags = %v, want {}", sent)
			}

			var got LaunchTemplateModel
			resp.State.Get(context.Background(), &got)
			if !got.Tags.Equal(tc.plan) {
				t.Errorf("state tags = %v after update, want the planned %v (inconsistent result after apply)", got.Tags, tc.plan)
			}
		})
	}
}
