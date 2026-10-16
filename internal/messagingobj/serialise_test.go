package messagingobj_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj/messagingobjtest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/messaging_permission"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/messaging_user"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/messaging_vhost"
)

func configured(t *testing.T, r resource.Resource, url string) resource.Resource {
	t.Helper()
	rc, ok := r.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatal("resource does not implement Configure")
	}
	var resp resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: messagingobjtest.Client(t, url)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", resp.Diagnostics)
	}
	return r
}

func create(t *testing.T, r resource.Resource, vals map[string]string) {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, vals)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("create %v: %v", vals, resp.Diagnostics)
	}
}

// THE reason for the shared lock: a vhost, a user and a grant created at once
// on ONE instance — as `terraform apply -parallelism=10` does for unrelated
// resources — must reach the platform one operation at a time, across all
// three kinds, while another instance proceeds alongside. The fake holds every
// request for a while, so without the lock the operations' open windows
// overlap and MaxOpen exceeds 1.
func TestAllThreeResourcesShareOnePerInstanceLock(t *testing.T) {
	fake := &messagingobjtest.Fake{Delay: 30 * time.Millisecond}
	fake.Seed("user", "inst-a", "seeded", "ready")
	fake.Seed("vhost", "inst-a", "seeded", "ready")
	srv := messagingobjtest.NewServer(fake)
	defer srv.Close()

	var wg sync.WaitGroup
	run := func(r resource.Resource, vals map[string]string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			create(t, configured(t, r, srv.URL), vals)
		}()
	}
	run(messaging_vhost.NewResource(), map[string]string{"instance_id": "inst-a", "name": "v1"})
	run(messaging_vhost.NewResource(), map[string]string{"instance_id": "inst-a", "name": "v2"})
	run(messaging_user.NewResource(), map[string]string{"instance_id": "inst-a", "name": "u1"})
	run(messaging_permission.NewResource(), map[string]string{
		"instance_id": "inst-a", "user": "seeded", "vhost": "seeded",
		"configure": ".*", "write": ".*", "read": ".*",
	})
	// Another instance: free to overlap with inst-a.
	run(messaging_vhost.NewResource(), map[string]string{"instance_id": "inst-b", "name": "v1"})
	wg.Wait()

	if got := fake.MaxOpen("inst-a"); got != 1 {
		t.Fatalf("%d operations were open on one instance at once; the per-instance lock must keep it at 1", got)
	}
	for _, o := range []struct{ kind, name string }{{"vhost", "v1"}, {"vhost", "v2"}, {"user", "u1"}, {"permission", "seeded/seeded"}} {
		if !fake.Has(o.kind, "inst-a", o.name) {
			t.Errorf("%s %s was not created", o.kind, o.name)
		}
	}
}
