package messaging_permission

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj/messagingobjtest"
)

func TestReadPermission(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	srv := messagingobjtest.NewServer(fake)
	defer srv.Close()

	d := &permissionDataSource{client: messagingobjtest.Client(t, srv.URL)}
	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(context.Background())
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, map[string]string{
		"instance_id": "inst-1", "user": "app", "vhost": "orders",
	})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	var m permissionModel
	if diags := resp.State.Get(context.Background(), &m); diags.HasError() {
		t.Fatal(diags)
	}
	if m.ID.ValueString() != "inst-1/app/orders" || m.Configure.ValueString() != ".*" || m.Status.ValueString() != "ready" {
		t.Errorf("got id %q configure %q status %q", m.ID.ValueString(), m.Configure.ValueString(), m.Status.ValueString())
	}
}
