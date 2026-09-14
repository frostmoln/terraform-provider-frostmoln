package tenant_default_tags

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

const otherTenant = "99999999-9999-4999-8999-999999999999"

func tdSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func tdType(t *testing.T) tftypes.Object {
	t.Helper()
	return tdSchema(t).Type().TerraformType(context.Background()).(tftypes.Object)
}

var (
	strNull = tftypes.NewValue(tftypes.String, nil)
	unknown = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
)

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func tagsValue(m map[string]string) tftypes.Value {
	elems := make(map[string]tftypes.Value, len(m))
	for k, v := range m {
		elems[k] = str(v)
	}
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, elems)
}

func obj(t *testing.T, id, tenant tftypes.Value, tags map[string]string) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(tdType(t), map[string]tftypes.Value{
		"id":        id,
		"tenant_id": tenant,
		"tags":      tagsValue(tags),
	})
}

func configured(t *testing.T, f *tagsettingstest.Fake) *tenantDefaultTagsResource {
	t.Helper()
	c := client.NewClient(f.Server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	f.Reset()
	return &tenantDefaultTagsResource{client: c}
}

func diagsText(d diag.Diagnostics) string {
	var b strings.Builder
	for _, e := range d {
		b.WriteString(e.Severity().String() + " " + e.Summary() + ": " + e.Detail() + "\n")
	}
	return b.String()
}

func mustOK(t *testing.T, what string, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() {
		t.Fatalf("%s: unexpected error:\n%s", what, diagsText(d))
	}
}

func create(t *testing.T, r *tenantDefaultTagsResource, plan tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := tdSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(tdType(t), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}}, resp)
	return resp
}

func stateOf(t *testing.T, st tfsdk.State) (tenant string, tags map[string]string) {
	t.Helper()
	var m TenantDefaultTagsModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	tags = map[string]string{}
	if d := m.Tags.ElementsAs(context.Background(), &tags, false); d.HasError() {
		t.Fatalf("tags: %v", d.Errors())
	}
	if m.ID != m.TenantID {
		t.Errorf("id %v must equal tenant_id %v", m.ID, m.TenantID)
	}
	return m.TenantID.ValueString(), tags
}

// assertPutBody checks a recorded PUT carried exactly {"tags": want}.
func assertPutBody(t *testing.T, req tagsettingstest.Request, want map[string]string) {
	t.Helper()
	body := req.BodyMap(t)
	if len(body) != 1 {
		t.Fatalf("PUT body = %s, want exactly one field, tags", req.Body)
	}
	raw, present := body["tags"]
	if !present || raw == nil {
		t.Fatalf("PUT body = %s: `tags` must always be an object — absent is refused, and {} is how a set is cleared", req.Body)
	}
	got := map[string]string{}
	for k, v := range raw.(map[string]any) {
		got[k], _ = v.(string)
	}
	if !tftags.Equal(got, want) {
		t.Fatalf("PUT tags = %v, want %v", got, want)
	}
}

func TestCreate_OmittedTenantWritesTheProviderTenantsWholeSet(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	want := map[string]string{"env": "prod", "cost-center": "eu-42"}

	resp := create(t, r, obj(t, unknown, unknown, want))
	mustOK(t, "create", resp.Diagnostics)

	puts := f.Only("PUT")
	if len(puts) != 1 || puts[0].Path != "/v1/tenants/"+f.TenantID+"/default-tags" {
		t.Fatalf("want one PUT of the provider tenant's set, got %+v", f.Requests())
	}
	assertPutBody(t, puts[0], want)
	tenant, tags := stateOf(t, resp.State)
	if tenant != f.TenantID || !tftags.Equal(tags, want) {
		t.Errorf("state = %s %v", tenant, tags)
	}
	if len(resp.Diagnostics) != 0 {
		t.Errorf("an empty prior set is not worth a warning, got:\n%s", diagsText(resp.Diagnostics))
	}
}

func TestCreate_EmptySetIsSentAsAnEmptyObject(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	resp := create(t, r, obj(t, unknown, unknown, map[string]string{}))
	mustOK(t, "create", resp.Diagnostics)
	assertPutBody(t, f.Only("PUT")[0], map[string]string{})
}

// The tenant already has defaults (from the portal). The resource adopts the
// one set — but says what it replaced instead of doing it silently.
func TestCreate_ReplacingAnExistingSetWarnsNamingTheKeys(t *testing.T) {
	f := tagsettingstest.New(t)
	f.SetDefaults(f.TenantID, map[string]string{"env": "dev", "owner": "ops", "keep": "same"})
	r := configured(t, f)

	resp := create(t, r, obj(t, unknown, unknown, map[string]string{"env": "prod", "keep": "same"}))
	mustOK(t, "create", resp.Diagnostics)

	if got := f.Defaults(f.TenantID); !tftags.Equal(got, map[string]string{"env": "prod", "keep": "same"}) {
		t.Errorf("the platform must hold exactly the configured set, got %v", got)
	}
	if len(resp.Diagnostics.Warnings()) != 1 {
		t.Fatalf("want one warning naming the replaced keys, got:\n%s", diagsText(resp.Diagnostics))
	}
	w := resp.Diagnostics.Warnings()[0].Detail()
	for _, want := range []string{`env="dev"`, `owner="ops"`, f.TenantID} {
		if !strings.Contains(w, want) {
			t.Errorf("the warning must name %s, got: %s", want, w)
		}
	}
	if strings.Contains(w, `keep="same"`) {
		t.Errorf("an unchanged key was not replaced and must not be named: %s", w)
	}
}

// A failed read of the set a create replaces must be SAID, not skipped: the
// create still writes (the PUT is authoritative), with a warning.
func TestCreate_PreviousSetUnreadableWarnsInsteadOfSkipping(t *testing.T) {
	f := tagsettingstest.New(t)
	f.FailDefaultsGet = true
	r := configured(t, f)

	resp := create(t, r, obj(t, unknown, unknown, map[string]string{"env": "prod"}))
	mustOK(t, "create", resp.Diagnostics)
	if len(f.Only("PUT")) != 1 {
		t.Fatalf("the create must still write, got %+v", f.Requests())
	}
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 || !strings.Contains(ws[0].Detail(), "Could not read") {
		t.Fatalf("want one warning that the previous set could not be read, got:\n%s", diagsText(resp.Diagnostics))
	}
}

func TestReadUpdateDelete_Lifecycle(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	s := tdSchema(t)
	ctx := context.Background()

	created := create(t, r, obj(t, unknown, unknown, map[string]string{"env": "prod", "team": "web"}))
	mustOK(t, "create", created.Diagnostics)

	// A key added in the portal shows up on refresh, so the plan can remove it.
	f.SetDefaults(f.TenantID, map[string]string{"env": "prod", "team": "web", "portal": "p"})
	read := &resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, read)
	mustOK(t, "read", read.Diagnostics)
	if _, tags := stateOf(t, read.State); tags["portal"] != "p" {
		t.Errorf("refresh must read the platform's set back, got %v", tags)
	}

	// Update writes the WHOLE set: the dropped key is absent from the body.
	f.Reset()
	plan := obj(t, str(f.TenantID), str(f.TenantID), map[string]string{"env": "staging"})
	upd := &resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{State: read.State, Plan: tfsdk.Plan{Schema: s, Raw: plan}}, upd)
	mustOK(t, "update", upd.Diagnostics)
	assertPutBody(t, f.Only("PUT")[0], map[string]string{"env": "staging"})
	if got := f.Defaults(f.TenantID); !tftags.Equal(got, map[string]string{"env": "staging"}) {
		t.Errorf("after update the platform holds %v", got)
	}

	// Destroy clears the set with an explicit {}.
	f.Reset()
	del := &resource.DeleteResponse{State: upd.State}
	r.Delete(ctx, resource.DeleteRequest{State: upd.State}, del)
	mustOK(t, "delete", del.Diagnostics)
	assertPutBody(t, f.Only("PUT")[0], map[string]string{})
	if got := f.Defaults(f.TenantID); len(got) != 0 {
		t.Errorf("after destroy the tenant must have no defaults, got %v", got)
	}
}

func TestRead_RefusalIsAnErrorAndKeepsState(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	st := tfsdk.State{Schema: tdSchema(t), Raw: obj(t, str(otherTenant), str(otherTenant), map[string]string{"env": "prod"})}
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatalf("a 403 must be an error that keeps state; diags:\n%s", diagsText(resp.Diagnostics))
	}
}

func TestImport(t *testing.T) {
	f := tagsettingstest.New(t)
	f.SetDefaults(f.TenantID, map[string]string{"env": "prod"})
	r := configured(t, f)
	s := tdSchema(t)
	ctx := context.Background()

	importID := func(id string) *resource.ImportStateResponse {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(tdType(t), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		return resp
	}

	for _, bad := range []string{"", "..", "not-a-uuid", f.TenantID + "/x"} {
		if importID(bad).Diagnostics.HasError() == false {
			t.Errorf("import id %q must be refused", bad)
		}
	}
	// Another tenant's defaults are not this resource's to manage: the resource
	// is tied to the provider's tenant.
	if resp := importID(otherTenant); !resp.Diagnostics.HasError() ||
		!strings.Contains(diagsText(resp.Diagnostics), "not the provider's tenant") {
		t.Errorf("importing another tenant must be refused, got:\n%s", diagsText(resp.Diagnostics))
	}
	if len(f.Requests()) != 0 {
		t.Fatalf("a refused id must be refused before any request, got %+v", f.Requests())
	}

	// The provider's tenant in any spelling is accepted and recorded canonical.
	imp := importID(strings.ToUpper(f.TenantID))
	mustOK(t, "import", imp.Diagnostics)
	read := &resource.ReadResponse{State: imp.State}
	r.Read(ctx, resource.ReadRequest{State: imp.State}, read)
	mustOK(t, "read after import", read.Diagnostics)
	tenant, tags := stateOf(t, read.State)
	if tenant != f.TenantID || !tftags.Equal(tags, map[string]string{"env": "prod"}) {
		t.Errorf("imported state = %s %v, want the canonical provider tenant", tenant, tags)
	}
}

func modifyPlan(t *testing.T, r *tenantDefaultTagsResource, prior, planned, config tftypes.Value) *resource.ModifyPlanResponse {
	t.Helper()
	s := tdSchema(t)
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: planned}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: s, Raw: prior},
		Plan:   tfsdk.Plan{Schema: s, Raw: planned},
		Config: tfsdk.Config{Schema: s, Raw: config},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("ModifyPlan must never raise an error (it would block destroy), got:\n%s", diagsText(resp.Diagnostics))
	}
	return resp
}

func TestModifyPlan_CreateNamesTheProviderTenant(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	tags := map[string]string{"env": "prod"}
	null := tftypes.NewValue(tdType(t), nil)

	resp := modifyPlan(t, r, null, obj(t, unknown, unknown, tags), obj(t, strNull, strNull, tags))
	if want := obj(t, str(f.TenantID), str(f.TenantID), tags); !resp.Plan.Raw.Equal(want) {
		t.Errorf("planned %v, want the provider tenant named", resp.Plan.Raw)
	}
	if len(resp.Diagnostics) != 0 {
		t.Errorf("an empty current set is not worth a warning, got:\n%s", diagsText(resp.Diagnostics))
	}
}

// Before any data is lost: the plan lists the keys a create will remove or change.
func TestModifyPlan_CreateOverExistingDefaultsWarnsListingTheKeys(t *testing.T) {
	f := tagsettingstest.New(t)
	f.SetDefaults(f.TenantID, map[string]string{"env": "dev", "owner": "ops", "keep": "same"})
	r := configured(t, f)
	tags := map[string]string{"env": "prod", "keep": "same"}
	null := tftypes.NewValue(tdType(t), nil)

	resp := modifyPlan(t, r, null, obj(t, unknown, unknown, tags), obj(t, strNull, strNull, tags))
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 {
		t.Fatalf("want one plan warning, got:\n%s", diagsText(resp.Diagnostics))
	}
	w := ws[0].Detail()
	for _, want := range []string{`env="dev"`, `owner="ops"`, f.TenantID} {
		if !strings.Contains(w, want) {
			t.Errorf("the plan warning must name %s: %s", want, w)
		}
	}
	if strings.Contains(w, `keep="same"`) {
		t.Errorf("a key kept as it is must not be listed: %s", w)
	}
	if got := f.Defaults(f.TenantID); len(got) != 3 {
		t.Errorf("planning must change nothing, the tenant now holds %v", got)
	}

	// With the new set not known yet, every current key may be replaced.
	unknownTags := tftypes.NewValue(tdType(t), map[string]tftypes.Value{
		"id": unknown, "tenant_id": unknown,
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, tftypes.UnknownValue),
	})
	resp = modifyPlan(t, r, null, unknownTags, unknownTags)
	if ws := resp.Diagnostics.Warnings(); len(ws) != 1 || !strings.Contains(ws[0].Detail(), `keep="same"`) {
		t.Errorf("with unknown tags every current key must be listed, got:\n%s", diagsText(resp.Diagnostics))
	}
}

func TestModifyPlan_CreateWithUnreadableDefaultsWarns(t *testing.T) {
	f := tagsettingstest.New(t)
	f.FailDefaultsGet = true
	r := configured(t, f)
	tags := map[string]string{"env": "prod"}
	null := tftypes.NewValue(tdType(t), nil)

	resp := modifyPlan(t, r, null, obj(t, unknown, unknown, tags), obj(t, strNull, strNull, tags))
	if ws := resp.Diagnostics.Warnings(); len(ws) != 1 || !strings.Contains(ws[0].Detail(), "Could not read") {
		t.Errorf("an unreadable current set must be said at plan, got:\n%s", diagsText(resp.Diagnostics))
	}
}

// The provider now points at another tenant: replace, with a warning naming
// both, so the old tenant's set is never cleared silently.
func TestModifyPlan_ProviderTenantChangedReplacesAndWarns(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	tags := map[string]string{"env": "prod"}
	prior := obj(t, str(otherTenant), str(otherTenant), tags)

	resp := modifyPlan(t, r, prior, prior, obj(t, strNull, strNull, tags))
	if len(resp.RequiresReplace) != 1 || !strings.Contains(resp.RequiresReplace[0].String(), "tenant_id") {
		t.Fatalf("a changed provider tenant must replace, got %v", resp.RequiresReplace)
	}
	if want := obj(t, str(f.TenantID), str(f.TenantID), tags); !resp.Plan.Raw.Equal(want) {
		t.Errorf("planned %v, want the provider tenant", resp.Plan.Raw)
	}
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 || !strings.Contains(ws[0].Detail(), otherTenant) || !strings.Contains(ws[0].Detail(), f.TenantID) {
		t.Errorf("want one warning naming both tenants, got:\n%s", diagsText(resp.Diagnostics))
	}

	// The same tenant in another spelling is the same tenant.
	same := obj(t, str(strings.ToUpper(f.TenantID)), str(strings.ToUpper(f.TenantID)), tags)
	if resp := modifyPlan(t, r, same, same, obj(t, strNull, strNull, tags)); len(resp.RequiresReplace) != 0 || len(resp.Diagnostics) != 0 {
		t.Errorf("the same tenant must neither replace nor warn: %v\n%s", resp.RequiresReplace, diagsText(resp.Diagnostics))
	}
}

func TestModifyPlan_DestroyOfAnotherTenantsSetWarns(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	tags := map[string]string{"env": "prod"}
	null := tftypes.NewValue(tdType(t), nil)

	resp := modifyPlan(t, r, obj(t, str(otherTenant), str(otherTenant), tags), null, null)
	if ws := resp.Diagnostics.Warnings(); len(ws) != 1 || !strings.Contains(ws[0].Detail(), otherTenant) {
		t.Errorf("destroying another tenant's set must warn, got:\n%s", diagsText(resp.Diagnostics))
	}
	resp = modifyPlan(t, r, obj(t, str(f.TenantID), str(f.TenantID), tags), null, null)
	if len(resp.Diagnostics) != 0 || len(f.Requests()) != 0 {
		t.Errorf("an ordinary destroy plan must neither warn nor call out: %s %+v", diagsText(resp.Diagnostics), f.Requests())
	}
}
