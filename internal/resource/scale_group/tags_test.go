package scale_group

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

// TestUpdateClearsTags: removing every tag must reach the scale-group PATCH as
// `"tags": {}`.
//
// PATCH /scale-groups/{id} is routed to provisioning, which forwards tags to
// compute only when the field is non-nil — "Non-nil rather than non-empty, so
// an empty map can CLEAR the tags" (provisioning/internal/handler/http/
// scale_group_handler.go, UpdateScaleGroup) — and compute replaces on the same
// condition (compute/internal/service/impl/scale_group.go, Update). The
// update struct carried `omitempty`, which drops an empty map, so the clear
// never left the provider and the read-back returned the old tags.
//
// The fake replaces its stored tags only on a non-null `tags`, the backend's
// own condition, so an omitted field leaves them in place just as it would in
// production.
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
				case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
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
					_ = json.NewEncoder(w).Encode(map[string]string{"scaleGroupId": "asg-1", "status": "updating"})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
					out := sgJSON("active")
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
			r := &scaleGroupResource{client: c}

			stateModel := fullSGModel(t)
			stateModel.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})
			planModel := fullSGModel(t)
			planModel.Tags = tc.plan

			state := buildSGState(t, stateModel)
			plan := buildSGPlan(t, planModel)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
			}

			if !patched {
				t.Fatal("no PATCH was sent")
			}
			if !hadTags || tagsNull {
				t.Fatalf("PATCH body carried no non-null `tags` (present=%v, null=%v); the backend keeps the old tags", hadTags, tagsNull)
			}
			if len(sent) != 0 {
				t.Errorf("PATCH tags = %v, want {}", sent)
			}

			var got ScaleGroupModel
			resp.State.Get(context.Background(), &got)
			if !got.Tags.Equal(tc.plan) {
				t.Errorf("state tags = %v after update, want the planned %v (inconsistent result after apply)", got.Tags, tc.plan)
			}
		})
	}
}
