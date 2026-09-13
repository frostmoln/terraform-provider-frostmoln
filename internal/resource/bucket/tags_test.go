package bucket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// TestBucketReadUntaggedDropsPriorTags: a bucket whose tags were removed
// outside Terraform must NOT keep its old tags in state on refresh.
//
// storage serialises a bucket's tags with omitempty
// (storage/internal/domain/bucket.go), so an untagged bucket's GET has no
// `tags` key at all. fromAPI used to treat that absence as "keep whatever the
// model had", which put the stale {env=prod} back into state: the next plan
// compared config {env=prod} against state {env=prod} and showed nothing, while
// the real bucket had no tags. Drift was invisible.
func TestBucketReadUntaggedDropsPriorTags(t *testing.T) {
	var sawGet bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/tenants/t-1/buckets/tagged" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawGet = true
		// No `tags` key: exactly what an untagged bucket's GET carries.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "tagged", "region": "sweden", "defaultStorageClass": "standard",
			"versioning": "disabled", "objectCount": 0, "totalSize": 0,
			"createdAt": "2025-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &bucketResource{client: c}

	priorTags, d := types.MapValueFrom(context.Background(), types.StringType, map[string]string{"env": "prod"})
	if d.HasError() {
		t.Fatalf("fixture: %v", d)
	}
	state := tfsdk.State{Schema: bucketSchema(t)}
	if d := state.Set(context.Background(), &BucketModel{
		Name:         types.StringValue("tagged"),
		Region:       types.StringValue("sweden"),
		StorageClass: types.StringValue("standard"),
		Versioning:   types.StringValue("disabled"),
		Tags:         priorTags,
		ObjectCount:  types.Int64Value(0),
		SizeBytes:    types.Int64Value(0),
		CreatedAt:    types.StringValue("2025-01-01T00:00:00Z"),
		TagsAll:      types.MapNull(types.StringType),
	}); d.HasError() {
		t.Fatalf("fixture: %v", d)
	}

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	if !sawGet {
		t.Fatal("Read never called the API")
	}

	var got BucketModel
	resp.State.Get(context.Background(), &got)
	if n := len(got.Tags.Elements()); n != 0 {
		t.Errorf("state tags = %v after refreshing an untagged bucket; the prior tags were kept, so the removal is invisible to plan", got.Tags)
	}
}
