package volume

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
)

// TestUpdateClearsTags: removing every tag must reach storage as
// `"metadata": {}`.
//
// PATCH /volumes/{id} goes straight to storage, which acts on metadata only
// when the field is non-nil, strips reserved keys from it and re-stamps the
// volume's existing reserved keys, then hands the result to Cinder, which
// applies volume metadata as a REPLACE (storage/internal/service/impl/
// volume.go, Update + sanitizeReservedVolumeMetadata; repository/cinder/
// volume.go, Update). So {} arrives at Cinder as "the reserved keys and nothing
// else": the user tags go, the platform keys stay. The update struct carried
// `omitempty`, which drops an empty map, so the clear never left the provider.
//
// The fake models that whole contract: an absent or null `metadata` keeps
// everything; a present one replaces the user keys while the reserved keys the
// volume already carries survive. Its GET returns them unfiltered, as the real
// customer GET does, so the read-back also exercises reservedmeta.
func TestUpdateClearsTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan types.Map
	}{
		{"tags block removed", types.MapNull(types.StringType)},
		{"tags set to empty map", types.MapValueMust(types.StringType, map[string]attr.Value{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := map[string]string{"a": "b", "customer-id": "t-1", "frostmoln_type": "customer"}
			var patched, hadMeta, metaNull bool
			var sent map[string]string

			vol := func() apiVolume {
				return apiVolume{
					ID: "vol-1", Name: "data", Size: 10, VolumeType: "ssd", Status: "available",
					CreatedAt: "2025-01-01T00:00:00Z", Metadata: stored,
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/volumes/vol-1":
					patched = true
					raw, _ := io.ReadAll(r.Body)
					var env map[string]json.RawMessage
					if err := json.Unmarshal(raw, &env); err != nil {
						t.Errorf("PATCH body is not a JSON object: %s", raw)
					}
					field, ok := env["metadata"]
					hadMeta = ok
					metaNull = ok && string(field) == "null"
					if ok && !metaNull {
						sent = map[string]string{}
						_ = json.Unmarshal(field, &sent)
						next := map[string]string{}
						for k, v := range sent {
							if !reservedmeta.IsReservedVolume(k) {
								next[k] = v
							}
						}
						for k, v := range stored {
							if reservedmeta.IsReservedVolume(k) {
								next[k] = v
							}
						}
						stored = next
					}
					_ = json.NewEncoder(w).Encode(vol())
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/volumes/vol-1":
					_ = json.NewEncoder(w).Encode(vol())
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := &volumeResource{client: c}

			base := VolumeModel{
				ID: types.StringValue("vol-1"), Name: types.StringValue("data"), Description: types.StringNull(),
				SizeGB: types.Int64Value(10), VolumeType: types.StringValue("ssd"), Zone: types.StringNull(),
				SnapshotID: types.StringNull(), Encrypted: types.BoolValue(false), Status: types.StringValue("available"),
				IOPS: types.Int64Value(0), Throughput: types.Int64Value(0), AttachedTo: types.StringNull(),
				DevicePath: types.StringNull(), CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
				TagsAll: types.MapNull(types.StringType),
			}
			stateModel := base
			stateModel.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})
			planModel := base
			planModel.Tags = tc.plan

			schema := getVolumeSchema(t).Schema
			state := tfsdk.State{Schema: schema}
			if d := state.Set(context.Background(), &stateModel); d.HasError() {
				t.Fatalf("fixture: %v", d)
			}
			plan := tfsdk.Plan{Schema: schema}
			if d := plan.Set(context.Background(), &planModel); d.HasError() {
				t.Fatalf("fixture: %v", d)
			}

			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
			}

			if !patched {
				t.Fatal("no PATCH was sent")
			}
			if !hadMeta || metaNull {
				t.Fatalf("PATCH body carried no non-null `metadata` (present=%v, null=%v); storage keeps the old tags", hadMeta, metaNull)
			}
			if len(sent) != 0 {
				t.Errorf("PATCH metadata = %v, want {}", sent)
			}
			if stored["customer-id"] != "t-1" {
				t.Errorf("reserved metadata lost: %v", stored)
			}

			var got VolumeModel
			resp.State.Get(context.Background(), &got)
			if !got.Tags.Equal(tc.plan) {
				t.Errorf("state tags = %v after update, want the planned %v (inconsistent result after apply)", got.Tags, tc.plan)
			}
		})
	}
}
