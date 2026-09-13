package secret

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

// secretWire is a secret as the secrets service actually serialises it:
// domain.Secret tags `tags` WITHOUT omitempty and the repository normalises a
// nil map to {}, so an untagged secret's GET carries `"tags": {}` — never an
// absent key (secrets/internal/domain/secret.go, repository/postgres/secret.go).
func secretWire(tags map[string]string) map[string]any {
	if tags == nil {
		tags = map[string]string{}
	}
	raw, _ := json.Marshal(secretJSON("active"))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	out["tags"] = tags
	return out
}

// TestUpdateClearsTags: removing every tag must reach the secrets service as
// `"tags": {}`.
//
// The service replaces a secret's tags only when the field is non-nil
// (secrets/internal/service/impl/secret.go, Update: `if req.Tags != nil {
// secret.Tags = req.Tags }`). The provider sent nothing for a removed block and
// an `omitempty`-dropped {} for an emptied one, so the old tags survived and
// the read-back failed the apply with an inconsistent result.
//
// The fake replaces only on a non-null `tags`, the service's own condition.
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
			var put, hadTags, tagsNull bool
			var sent map[string]string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/secrets/secret-1":
					put = true
					raw, _ := io.ReadAll(r.Body)
					var env map[string]json.RawMessage
					if err := json.Unmarshal(raw, &env); err != nil {
						t.Errorf("PUT body is not a JSON object: %s", raw)
					}
					field, ok := env["tags"]
					hadTags = ok
					tagsNull = ok && string(field) == "null"
					if ok && !tagsNull {
						sent = map[string]string{}
						_ = json.Unmarshal(field, &sent)
						stored = sent
					}
					_ = json.NewEncoder(w).Encode(secretWire(stored))
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/secrets/secret-1":
					_ = json.NewEncoder(w).Encode(secretWire(stored))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := &secretResource{client: c}

			stateModel := fullSecretModel()
			stateModel.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})
			planModel := fullSecretModel()
			planModel.Tags = tc.plan

			state := buildSecretState(t, stateModel)
			plan := buildSecretPlan(t, planModel)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: buildSecretConfig(t, planModel)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
			}

			if !put {
				t.Fatal("no PUT was sent")
			}
			if !hadTags || tagsNull {
				t.Fatalf("PUT body carried no non-null `tags` (present=%v, null=%v); the service keeps the old tags", hadTags, tagsNull)
			}
			if len(sent) != 0 {
				t.Errorf("PUT tags = %v, want {}", sent)
			}

			var got SecretModel
			resp.State.Get(context.Background(), &got)
			if !got.Tags.Equal(tc.plan) {
				t.Errorf("state tags = %v after update, want the planned %v (inconsistent result after apply)", got.Tags, tc.plan)
			}
		})
	}
}

// TestReadEmptyTagsRoundTrips: `tags = {}` must read back as {}, not null.
// The service answers an untagged secret with `"tags": {}`, and fromAPI mapped
// every empty read to null, so a config saying {} failed every apply with an
// inconsistent result and every refresh planned a spurious change.
func TestReadEmptyTagsRoundTrips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(secretWire(nil))
	}))
	defer srv.Close()

	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &secretResource{client: c}

	model := fullSecretModel()
	model.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{})
	state := buildSecretState(t, model)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got SecretModel
	resp.State.Get(context.Background(), &got)
	if got.Tags.IsNull() {
		t.Error("`tags = {}` read back as null; the next apply reports an inconsistent result")
	}
	if n := len(got.Tags.Elements()); n != 0 {
		t.Errorf("state tags = %v, want {}", got.Tags)
	}
}
