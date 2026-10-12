package appgw_backend_authorization

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func tagMap(kv ...string) types.Map {
	elems := map[string]attr.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		elems[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, elems)
}

// An authorize that ADOPTS an existing rule keeps that rule's tags (the platform
// tags only a first creation). With adopt_existing the configured tags are then
// written onto it, keeping the keys it already carries, so the create returns
// what the plan promised.
func TestAdoptedAuthorizationGetsTheConfiguredTags(t *testing.T) {
	var patch map[string]map[string]string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			a := authzFixture(true)
			a.Tags = map[string]string{"owner": "team-a"}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(a)
		case http.MethodPatch:
			if r.URL.Path != authzBase+"/authorizations/a-1" {
				t.Errorf("PATCH %s, want the authorization", r.URL.Path)
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &patch)
			_ = json.NewEncoder(w).Encode(authzFixture(true))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ar := &authorizationResource{client: c}
	m := authzModel()
	m.AdoptExisting = types.BoolValue(true)
	m.Tags = tagMap("env", "prod")
	m.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	want := map[string]string{"owner": "team-a", "env": "prod"}
	if len(patch) != 1 || len(patch["tags"]) != 2 || patch["tags"]["env"] != "prod" || patch["tags"]["owner"] != "team-a" {
		t.Errorf("PATCH body = %v, want exactly {tags: %v}", patch, want)
	}
	var out AuthorizationModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &out)...)
	if !out.Tags.Equal(tagMap("env", "prod")) || !out.TagsAll.Equal(tagMap("env", "prod", "owner", "team-a")) {
		t.Errorf("state tags = %v / %v", out.Tags, out.TagsAll)
	}
}
