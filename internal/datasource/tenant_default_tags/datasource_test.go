package tenantdefaulttags

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
)

const otherTenant = "99999999-9999-4999-8999-999999999999"

func read(t *testing.T, f *tagsettingstest.Fake, tenant *string) *datasource.ReadResponse {
	t.Helper()
	c := client.NewClient(f.Server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	f.Reset()

	var sr datasource.SchemaResponse
	NewDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(context.Background())
	tv := tftypes.NewValue(tftypes.String, nil)
	if tenant != nil {
		tv = tftypes.NewValue(tftypes.String, *tenant)
	}
	cfg := tftypes.NewValue(typ, map[string]tftypes.Value{
		"tenant_id": tv,
		"tags":      tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
	})
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&tenantDefaultTagsDataSource{client: c}).Read(context.Background(),
		datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg}}, resp)
	return resp
}

func stateOf(t *testing.T, resp *datasource.ReadResponse) (string, map[string]string, bool) {
	t.Helper()
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var m tenantDefaultTagsModel
	if d := resp.State.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d.Errors())
	}
	tags := map[string]string{}
	m.Tags.ElementsAs(context.Background(), &tags, false)
	return m.TenantID.ValueString(), tags, m.Tags.IsNull()
}

func TestRead_ProviderTenantByDefault(t *testing.T) {
	f := tagsettingstest.New(t)
	f.SetDefaults(f.TenantID, map[string]string{"env": "prod"})
	tenant, tags, _ := stateOf(t, read(t, f, nil))
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Path != "/v1/tenants/"+f.TenantID+"/default-tags" {
		t.Fatalf("want one GET of the provider tenant's set, got %+v", reqs)
	}
	if tenant != f.TenantID || tags["env"] != "prod" || len(tags) != 1 {
		t.Errorf("state = %s %v", tenant, tags)
	}
}

func TestRead_ExplicitTenantAndEmptySetIsEmptyNotNull(t *testing.T) {
	f := tagsettingstest.New(t)
	f.AddTenant(otherTenant)
	tenant := otherTenant
	got, tags, isNull := stateOf(t, read(t, f, &tenant))
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Path != "/v1/tenants/"+otherTenant+"/default-tags" {
		t.Fatalf("want one GET of the configured tenant's set, got %+v", reqs)
	}
	if got != otherTenant || isNull || len(tags) != 0 {
		t.Errorf("state = %s %v (null %v), want an empty, non-null map", got, tags, isNull)
	}
}

func TestRead_RefusalIsAnError(t *testing.T) {
	f := tagsettingstest.New(t)
	tenant := otherTenant // not reachable
	if resp := read(t, f, &tenant); !resp.Diagnostics.HasError() {
		t.Fatal("a refused read must be an error")
	}
}
