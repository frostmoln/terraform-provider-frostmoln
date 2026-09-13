package tagcolors

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
)

func dsSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func read(t *testing.T, f *tagsettingstest.Fake, orgID *string) *datasource.ReadResponse {
	t.Helper()
	c := client.NewClient(f.Server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	f.Reset()

	s := dsSchema(t)
	typ := s.Type().TerraformType(context.Background())
	obj := typ.(tftypes.Object)
	org := tftypes.NewValue(tftypes.String, nil)
	if orgID != nil {
		org = tftypes.NewValue(tftypes.String, *orgID)
	}
	cfg := tftypes.NewValue(typ, map[string]tftypes.Value{
		"organization_id": org,
		"rules":           tftypes.NewValue(obj.AttributeTypes["rules"], nil),
	})
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	(&tagColorsDataSource{client: c}).Read(context.Background(),
		datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: cfg}}, resp)
	return resp
}

func state(t *testing.T, resp *datasource.ReadResponse) (string, bool, []ruleModel) {
	t.Helper()
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var m tagColorsModel
	if d := resp.State.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	var rules []ruleModel
	if d := m.Rules.ElementsAs(context.Background(), &rules, false); d.HasError() {
		t.Fatalf("rules: %v", d.Errors())
	}
	return m.OrganizationID.ValueString(), m.OrganizationID.IsNull(), rules
}

func ptr(s string) *string { return &s }

func TestRead_OmittedOrganizationUsesTheTenantRouteAndReportsIt(t *testing.T) {
	f := tagsettingstest.New(t)
	anyID := f.Seed(f.OrgID, "env", nil, "#111111")
	emptyID := f.Seed(f.OrgID, "env", ptr(""), "#222222")
	prodID := f.Seed(f.OrgID, "env", ptr("prod"), "#D73A49")

	resp := read(t, f, nil)
	org, orgNull, rules := state(t, resp)

	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Path != "/v1/tenants/"+f.TenantID+"/tag-colors" {
		t.Fatalf("want one GET of the tenant route, got %+v", reqs)
	}
	if orgNull || org != f.OrgID {
		t.Errorf("organization_id = %q (null %v), want %s", org, orgNull, f.OrgID)
	}
	if len(rules) != 3 {
		t.Fatalf("want 3 rules, got %d", len(rules))
	}
	// The server's order: the key-only rule first, then by value.
	if rules[0].ID.ValueString() != anyID || !rules[0].Value.IsNull() {
		t.Errorf("rule 0 = %+v, want the key-only rule with a NULL value", rules[0])
	}
	if rules[1].ID.ValueString() != emptyID || rules[1].Value.IsNull() || rules[1].Value.ValueString() != "" {
		t.Errorf("rule 1 = %+v, want the empty-value rule with value \"\" (not null)", rules[1])
	}
	if rules[2].ID.ValueString() != prodID || rules[2].Value.ValueString() != "prod" || rules[2].Color.ValueString() != "#D73A49" {
		t.Errorf("rule 2 = %+v", rules[2])
	}
}

func TestRead_ExplicitOrganizationUsesTheOrganizationRoute(t *testing.T) {
	f := tagsettingstest.New(t)
	other := "33333333-3333-4333-8333-333333333333"
	f.AddOrg(other)
	id := f.Seed(other, "team", nil, "#333333")
	f.Seed(f.OrgID, "env", nil, "#111111")

	org, _, rules := state(t, read(t, f, ptr(other)))
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Path != "/v1/organizations/"+other+"/tag-colors" {
		t.Fatalf("want one GET of the organization route, got %+v", reqs)
	}
	if org != other || len(rules) != 1 || rules[0].ID.ValueString() != id {
		t.Errorf("org %q rules %+v", org, rules)
	}
}

// A platform that does not report the owning organization still answers the
// rules; the data source lists them and leaves organization_id null rather than
// inventing one.
func TestRead_UnreportedOrganizationIsNullNotGuessed(t *testing.T) {
	f := tagsettingstest.New(t)
	f.ReportOrganization = false
	f.Seed(f.OrgID, "env", nil, "#111111")

	_, orgNull, rules := state(t, read(t, f, nil))
	if !orgNull {
		t.Error("organization_id must be null when the platform does not report it")
	}
	if len(rules) != 1 {
		t.Errorf("the rules must still be listed, got %d", len(rules))
	}
}

func TestRead_ErrorIsSurfaced(t *testing.T) {
	f := tagsettingstest.New(t)
	resp := read(t, f, ptr("55555555-5555-4555-8555-555555555555")) // not a member
	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused read must be an error")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(d, "PERMISSION_DENIED") {
		t.Errorf("the error must carry the platform's code, got %q", d)
	}
}
