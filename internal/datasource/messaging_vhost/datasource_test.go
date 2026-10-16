package messaging_vhost

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj/messagingobjtest"
)

func read(t *testing.T, fake *messagingobjtest.Fake, name string) (vhostModel, *datasource.ReadResponse) {
	t.Helper()
	srv := messagingobjtest.NewServer(fake)
	defer srv.Close()
	d := &vhostDataSource{client: messagingobjtest.Client(t, srv.URL)}
	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(context.Background())
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": name})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, resp)
	var m vhostModel
	if !resp.Diagnostics.HasError() {
		_ = resp.State.Get(context.Background(), &m)
	}
	return m, resp
}

func TestReadVhost(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("vhost", "inst-1", "orders", "ready")
	m, resp := read(t, fake, "orders")
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	if m.ID.ValueString() != "inst-1/orders" || m.Status.ValueString() != "ready" || !m.FailureReason.IsNull() {
		t.Errorf("got id %q status %q", m.ID.ValueString(), m.Status.ValueString())
	}
}

func TestReadMissingVhostIsAnError(t *testing.T) {
	if _, resp := read(t, &messagingobjtest.Fake{}, "nope"); !resp.Diagnostics.HasError() {
		t.Error("a missing vhost must be an error")
	}
}
