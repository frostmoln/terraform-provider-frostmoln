package tag_color

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/failclosed"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
)

// --- harness ---

func tcSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics.Errors())
	}
	return resp.Schema
}

func tcType(t *testing.T) tftypes.Object {
	t.Helper()
	obj, ok := tcSchema(t).Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return obj
}

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

var (
	null    = tftypes.NewValue(tftypes.String, nil)
	unknown = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
)

// obj builds a full object value: every attribute null unless overridden. The
// attribute set comes from the schema, so a new attribute cannot fall out.
func obj(t *testing.T, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	typ := tcType(t)
	attrs := make(map[string]tftypes.Value, len(typ.AttributeTypes))
	for name, at := range typ.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	for name, v := range overrides {
		if _, ok := typ.AttributeTypes[name]; !ok {
			t.Fatalf("%q is not an attribute", name)
		}
		attrs[name] = v
	}
	return tftypes.NewValue(typ, attrs)
}

// createPlan is what Terraform plans for a new rule: computed attributes
// unknown, organization_id unknown when omitted from the configuration.
func createPlan(t *testing.T, org, value tftypes.Value, key, color string) tftypes.Value {
	t.Helper()
	return obj(t, map[string]tftypes.Value{
		"id":              unknown,
		"organization_id": org,
		"key":             str(key),
		"value":           value,
		"color":           str(color),
		"created_at":      unknown,
		"updated_at":      unknown,
	})
}

func configured(t *testing.T, f *tagsettingstest.Fake) *tagColorResource {
	t.Helper()
	c := client.NewClient(f.Server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	f.Reset()
	return &tagColorResource{client: c}
}

func doCreate(t *testing.T, r *tagColorResource, plan tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := tcSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(tcType(t), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}}, resp)
	return resp
}

func doRead(t *testing.T, r *tagColorResource, state tftypes.Value) *resource.ReadResponse {
	t.Helper()
	s := tcSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: state}}, resp)
	return resp
}

func doUpdate(t *testing.T, r *tagColorResource, state, plan tftypes.Value) *resource.UpdateResponse {
	t.Helper()
	s := tcSchema(t)
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		State: tfsdk.State{Schema: s, Raw: state},
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
	}, resp)
	return resp
}

func doDelete(t *testing.T, r *tagColorResource, state tftypes.Value) *resource.DeleteResponse {
	t.Helper()
	s := tcSchema(t)
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: state}}, resp)
	return resp
}

func doImport(t *testing.T, r *tagColorResource, id string) *resource.ImportStateResponse {
	t.Helper()
	s := tcSchema(t)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(tcType(t), nil)}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
	return resp
}

func model(t *testing.T, st tfsdk.State) TagColorModel {
	t.Helper()
	var m TagColorModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return m
}

// diagsText renders every error diagnostic, summary and detail.
func diagsText(d diag.Diagnostics) string {
	var b strings.Builder
	for _, e := range d.Errors() {
		b.WriteString(e.Summary() + ": " + e.Detail() + "\n")
	}
	return b.String()
}

func mustOK(t *testing.T, what string, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() {
		t.Fatalf("%s: unexpected error:\n%s", what, diagsText(d))
	}
}

func ptr(s string) *string { return &s }

// assertValueOnWire checks how a recorded body carried `value`: present in
// every case (the PUT is a full replace, so an omitted value would silently
// mean "any value"), JSON null for a key-only rule, a string otherwise.
func assertValueOnWire(t *testing.T, req tagsettingstest.Request, want *string) {
	t.Helper()
	body := req.BodyMap(t)
	v, present := body["value"]
	if !present {
		t.Fatalf("%s %s: `value` was omitted from the body %s — it must always be sent", req.Method, req.Path, req.Body)
	}
	switch {
	case want == nil && v != nil:
		t.Fatalf("%s %s: value = %#v, want JSON null (a rule for ANY value) — body %s", req.Method, req.Path, v, req.Body)
	case want != nil && v == nil:
		t.Fatalf("%s %s: value = null, want %q — body %s", req.Method, req.Path, *want, req.Body)
	case want != nil && v != *want:
		t.Fatalf("%s %s: value = %#v, want %q", req.Method, req.Path, v, *want)
	}
}

// --- create ---

func TestCreate_NullValueGoesOutAsJSONNullAndColourCaseIsKept(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, str(f.OrgID), null, "env", "#D73A49"))
	mustOK(t, "create", resp.Diagnostics)

	posts := f.Only("POST")
	if len(posts) != 1 || posts[0].Path != "/v1/organizations/"+f.OrgID+"/tag-colors" {
		t.Fatalf("want one POST to the organization's collection, got %+v", f.Requests())
	}
	assertValueOnWire(t, posts[0], nil)
	body := posts[0].BodyMap(t)
	if body["key"] != "env" || body["color"] != "#D73A49" || len(body) != 3 {
		t.Errorf("create body = %s, want exactly key, value, color", posts[0].Body)
	}
	if n := len(f.Only("GET")); n != 0 {
		t.Errorf("organization_id was set, so nothing should be resolved; saw %d GETs", n)
	}

	m := model(t, resp.State)
	if !m.Value.IsNull() {
		t.Errorf("state value = %v, want null", m.Value)
	}
	if m.Color.ValueString() != "#D73A49" {
		t.Errorf("state color = %q, want the case as written (the platform keeps it)", m.Color.ValueString())
	}
	if m.OrganizationID.ValueString() != f.OrgID || m.ID.IsUnknown() || m.ID.ValueString() == "" {
		t.Errorf("state org/id = %v/%v", m.OrganizationID, m.ID)
	}
	if stored := f.Get(f.OrgID, m.ID.ValueString()); stored == nil || stored.Value != nil {
		t.Errorf("the platform must hold a key-only rule, got %+v", stored)
	}
	if m.CreatedAt.ValueString() == "" || m.UpdatedAt.ValueString() == "" {
		t.Error("timestamps must be set from the response")
	}
}

func TestCreate_EmptyValueGoesOutAsEmptyStringNotNull(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, str(f.OrgID), str(""), "env", "#1f6feb"))
	mustOK(t, "create", resp.Diagnostics)

	assertValueOnWire(t, f.Only("POST")[0], ptr(""))
	m := model(t, resp.State)
	if m.Value.IsNull() || m.Value.ValueString() != "" {
		t.Errorf("state value = %v, want \"\" (the empty-value rule, not the any-value rule)", m.Value)
	}
	if stored := f.Get(f.OrgID, m.ID.ValueString()); stored == nil || stored.Value == nil || *stored.Value != "" {
		t.Errorf("the platform must hold an empty-value rule, got %+v", stored)
	}
}

// A key-only rule and an empty-value rule are different rules, so creating the
// second while the first exists is NOT a conflict.
func TestCreate_EmptyValueRuleCoexistsWithKeyOnlyRule(t *testing.T) {
	f := tagsettingstest.New(t)
	f.Seed(f.OrgID, "env", nil, "#000000")
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, str(f.OrgID), str(""), "env", "#1f6feb"))
	mustOK(t, "create", resp.Diagnostics)
}

func TestCreate_OmittedOrganizationIsResolvedFromTheTenantRoute(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, unknown, str("prod"), "env", "#d73a49"))
	mustOK(t, "create", resp.Diagnostics)

	reqs := f.Requests()
	if len(reqs) != 2 ||
		reqs[0].Method != "GET" || reqs[0].Path != "/v1/tenants/"+f.TenantID+"/tag-colors" ||
		reqs[1].Method != "POST" || reqs[1].Path != "/v1/organizations/"+f.OrgID+"/tag-colors" {
		t.Fatalf("want GET the tenant's tag-colors, then POST to the organization it names; got %+v", reqs)
	}
	assertValueOnWire(t, reqs[1], ptr("prod"))
	if m := model(t, resp.State); m.OrganizationID.ValueString() != f.OrgID {
		t.Errorf("state organization_id = %v, want the tenant's owning organization %s", m.OrganizationID, f.OrgID)
	}
}

// A platform that does not name the owning organization must stop the create —
// never write the rule to some other organization (the home one) instead.
func TestCreate_OmittedOrganizationNotReportedIsAnErrorAndWritesNothing(t *testing.T) {
	f := tagsettingstest.New(t)
	f.ReportOrganization = false
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, unknown, null, "env", "#d73a49"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unresolvable organization must be an error")
	}
	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"organization_id", f.TenantID, "home organization"} {
		if !strings.Contains(text, want) {
			t.Errorf("the error must mention %q, got:\n%s", want, text)
		}
	}
	if posts := f.Only("POST"); len(posts) != 0 {
		t.Errorf("nothing may be written when the organization is unknown, got %+v", posts)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("no state may be recorded")
	}
}

func TestCreate_ExistingRuleIsAnErrorNamingTheImport(t *testing.T) {
	f := tagsettingstest.New(t)
	existing := f.Seed(f.OrgID, "env", ptr("prod"), "#000000")
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, str(f.OrgID), str("prod"), "env", "#d73a49"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("a create colliding with an existing rule must fail, not adopt it")
	}
	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"terraform import", f.OrgID + "/" + existing, "Already Exists", "re-run the apply", "`moved` block"} {
		if !strings.Contains(text, want) {
			t.Errorf("the 409 must name %q, got:\n%s", want, text)
		}
	}
	// The same-apply hint comes BEFORE the import: importing a rule another
	// resource is about to move off would take over the wrong rule.
	if strings.Index(text, "re-run the apply") > strings.Index(text, "terraform import") {
		t.Errorf("the re-run hint must precede the import hint:\n%s", text)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("the existing rule must not be adopted into state")
	}
	if got := f.Get(f.OrgID, existing); got.Color != "#000000" {
		t.Errorf("the existing rule must be untouched, got colour %q", got.Color)
	}
}

// --- read ---

func TestRead_RefreshesOutOfBandChangesAndKeepsTheOrganization(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), str("prod"), "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	id := model(t, created.State).ID.ValueString()

	f.Mutate(f.OrgID, id, func(rule *tagsettingstest.Rule) { rule.Value = nil; rule.Color = "#ABCDEF" })
	f.Reset()

	resp := doRead(t, r, created.State.Raw)
	mustOK(t, "read", resp.Diagnostics)
	if gets := f.Only("GET"); len(gets) != 1 || gets[0].Path != "/v1/organizations/"+f.OrgID+"/tag-colors/"+id {
		t.Fatalf("want one GET of the rule, got %+v", f.Requests())
	}
	m := model(t, resp.State)
	if !m.Value.IsNull() || m.Color.ValueString() != "#ABCDEF" {
		t.Errorf("refresh must read back value=null color=#ABCDEF, got %v %v", m.Value, m.Color)
	}
	if m.OrganizationID.ValueString() != f.OrgID {
		t.Errorf("organization_id must survive a refresh (the rule does not carry it), got %v", m.OrganizationID)
	}
}

func TestRead_ServiceNotFoundRemovesFromState(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), null, "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	f.Remove(f.OrgID, model(t, created.State).ID.ValueString())

	resp := doRead(t, r, created.State.Raw)
	mustOK(t, "read", resp.Diagnostics)
	if !resp.State.Raw.IsNull() {
		t.Error("a rule the platform says is gone must be removed from state")
	}
}

func TestRead_GatewayNotRoutedKeepsState(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), null, "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	f.NotRouted = true

	resp := doRead(t, r, created.State.Raw)
	failclosed.AssertReadKeepsState(t, "tag_color", resp.State.Raw.IsNull(), resp.Diagnostics.HasError())
}

// --- update ---

func TestUpdate_RetargetsKeyAndValueInPlaceWithAFullPUT(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), str("prod"), "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	before := model(t, created.State)
	f.Reset()

	plan := obj(t, map[string]tftypes.Value{
		"id":              str(before.ID.ValueString()),
		"organization_id": str(f.OrgID),
		"key":             str("team"),
		"value":           str(""),
		"color":           str("#1F6FEB"),
		"created_at":      str(before.CreatedAt.ValueString()),
		"updated_at":      unknown,
	})
	resp := doUpdate(t, r, created.State.Raw, plan)
	mustOK(t, "update", resp.Diagnostics)

	puts := f.Only("PUT")
	if len(puts) != 1 || puts[0].Path != "/v1/organizations/"+f.OrgID+"/tag-colors/"+before.ID.ValueString() {
		t.Fatalf("want one PUT of the same rule, got %+v", f.Requests())
	}
	if n := len(f.Only("POST")) + len(f.Only("DELETE")); n != 0 {
		t.Errorf("a key/value change is an in-place update, but saw %d create/delete calls", n)
	}
	assertValueOnWire(t, puts[0], ptr(""))
	if body := puts[0].BodyMap(t); body["key"] != "team" || body["color"] != "#1F6FEB" {
		t.Errorf("PUT body = %s", puts[0].Body)
	}
	after := model(t, resp.State)
	if after.ID != before.ID || after.OrganizationID.ValueString() != f.OrgID {
		t.Errorf("id/org must be unchanged: %v/%v", after.ID, after.OrganizationID)
	}
	if after.Key.ValueString() != "team" || after.Value.IsNull() || after.Value.ValueString() != "" {
		t.Errorf("state after update = key %v value %v", after.Key, after.Value)
	}
	if after.UpdatedAt == before.UpdatedAt {
		t.Error("updated_at must be read back from the PUT response")
	}
}

// Removing `value` from the configuration must send an explicit null. The
// field omitted would mean the same to today's server, but the PUT is a full
// replace and the body should state the whole rule, not lean on what absence
// means.
func TestUpdate_RemovingValueSendsNull(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), str("prod"), "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	before := model(t, created.State)
	f.Reset()

	plan := obj(t, map[string]tftypes.Value{
		"id":              str(before.ID.ValueString()),
		"organization_id": str(f.OrgID),
		"key":             str("env"),
		"value":           null,
		"color":           str("#d73a49"),
		"created_at":      str(before.CreatedAt.ValueString()),
		"updated_at":      unknown,
	})
	resp := doUpdate(t, r, created.State.Raw, plan)
	mustOK(t, "update", resp.Diagnostics)
	assertValueOnWire(t, f.Only("PUT")[0], nil)
	if m := model(t, resp.State); !m.Value.IsNull() {
		t.Errorf("state value = %v, want null", m.Value)
	}
}

func TestUpdate_RetargetOntoAnotherRuleIsAClearError(t *testing.T) {
	f := tagsettingstest.New(t)
	other := f.Seed(f.OrgID, "team", nil, "#000000")
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), null, "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	before := model(t, created.State)

	plan := obj(t, map[string]tftypes.Value{
		"id":              str(before.ID.ValueString()),
		"organization_id": str(f.OrgID),
		"key":             str("team"),
		"value":           null,
		"color":           str("#d73a49"),
		"created_at":      str(before.CreatedAt.ValueString()),
		"updated_at":      unknown,
	})
	resp := doUpdate(t, r, created.State.Raw, plan)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a retarget onto an existing rule must fail")
	}
	text := diagsText(resp.Diagnostics)
	for _, want := range []string{other, "re-run the apply", "`moved` block"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal must say %q, got:\n%s", want, text)
		}
	}
}

// --- delete ---

func TestDelete_DeletesAndToleratesAlreadyGone(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), null, "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	id := model(t, created.State).ID.ValueString()
	f.Reset()

	mustOK(t, "delete", doDelete(t, r, created.State.Raw).Diagnostics)
	if dels := f.Only("DELETE"); len(dels) != 1 || dels[0].Path != "/v1/organizations/"+f.OrgID+"/tag-colors/"+id {
		t.Fatalf("want one DELETE of the rule, got %+v", f.Requests())
	}
	if f.Get(f.OrgID, id) != nil {
		t.Error("the rule must be gone")
	}

	// Again: the flat 404 means it is already gone, which is what destroy wants.
	mustOK(t, "second delete", doDelete(t, r, created.State.Raw).Diagnostics)
}

func TestDelete_GatewayNotRoutedDoesNotReportSuccess(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := doCreate(t, r, createPlan(t, str(f.OrgID), null, "env", "#d73a49"))
	mustOK(t, "create", created.Diagnostics)
	f.NotRouted = true

	failclosed.AssertDeleteDoesNotReportSuccess(t, "tag_color", doDelete(t, r, created.State.Raw).Diagnostics.HasError())
}

// --- import ---

func TestImport_CompositeIDThenReadDistinguishesNullFromEmpty(t *testing.T) {
	f := tagsettingstest.New(t)
	other := "33333333-3333-4333-8333-333333333333"
	f.AddOrg(other)
	anyID := f.Seed(other, "env", nil, "#111111")
	emptyID := f.Seed(other, "env", ptr(""), "#222222")
	r := configured(t, f)

	for _, tc := range []struct {
		id        string
		wantValue *string
	}{{anyID, nil}, {emptyID, ptr("")}} {
		imp := doImport(t, r, other+"/"+tc.id)
		mustOK(t, "import", imp.Diagnostics)
		var gotOrg, gotID types.String
		imp.State.GetAttribute(context.Background(), path.Root("organization_id"), &gotOrg)
		imp.State.GetAttribute(context.Background(), path.Root("id"), &gotID)
		if gotOrg.ValueString() != other || gotID.ValueString() != tc.id {
			t.Fatalf("import state org/id = %v/%v", gotOrg, gotID)
		}
		if n := len(f.Only("GET")); n != 0 {
			t.Errorf("a composite id needs no resolution, saw %d GETs", n)
		}

		read := doRead(t, r, imp.State.Raw)
		mustOK(t, "read after import", read.Diagnostics)
		m := model(t, read.State)
		switch {
		case tc.wantValue == nil && !m.Value.IsNull():
			t.Errorf("imported key-only rule: value = %v, want null", m.Value)
		case tc.wantValue != nil && (m.Value.IsNull() || m.Value.ValueString() != *tc.wantValue):
			t.Errorf("imported empty-value rule: value = %v, want \"\"", m.Value)
		}
		f.Reset()
	}
}

func TestImport_BareIDResolvesTheTenantsOrganization(t *testing.T) {
	f := tagsettingstest.New(t)
	id := f.Seed(f.OrgID, "env", nil, "#111111")
	r := configured(t, f)

	imp := doImport(t, r, id)
	mustOK(t, "import", imp.Diagnostics)
	var gotOrg types.String
	imp.State.GetAttribute(context.Background(), path.Root("organization_id"), &gotOrg)
	if gotOrg.ValueString() != f.OrgID {
		t.Errorf("organization_id = %v, want the tenant's owning organization", gotOrg)
	}
	if gets := f.Only("GET"); len(gets) != 1 || gets[0].Path != "/v1/tenants/"+f.TenantID+"/tag-colors" {
		t.Errorf("want the tenant route consulted once, got %+v", f.Requests())
	}
}

func TestImport_BareIDWithoutAReportedOrganizationFails(t *testing.T) {
	f := tagsettingstest.New(t)
	f.ReportOrganization = false
	id := f.Seed(f.OrgID, "env", nil, "#111111")
	r := configured(t, f)

	imp := doImport(t, r, id)
	if !imp.Diagnostics.HasError() {
		t.Fatal("a bare id whose organization cannot be resolved must fail")
	}
	if text := diagsText(imp.Diagnostics); !strings.Contains(text, "<organization_id>/<rule_id>") {
		t.Errorf("the error must point at the composite form, got:\n%s", text)
	}
}

func TestImport_RefusesMalformedIDsBeforeAnyRequest(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	const org = "22222222-2222-4222-8222-222222222222"
	const rule = "44444444-4444-4444-8444-444444444444"
	for _, id := range []string{
		"", "..", ".", org + "/..", "../" + rule, org + "/" + rule + "/x", "/" + rule, org + "/",
		"not-a-uuid", org + "/not-a-uuid", "not-a-uuid/" + rule,
	} {
		if imp := doImport(t, r, id); !imp.Diagnostics.HasError() {
			t.Errorf("import id %q must be refused", id)
		}
	}
	if reqs := f.Requests(); len(reqs) != 0 {
		t.Errorf("a malformed id must be refused before any request, got %+v", reqs)
	}
}

// --- resolution failures ---

// A key without organizations:read is refused on the tenant route. The error
// must say that reads need the scope too — setting organization_id would skip
// this lookup, but every refresh of the rule reads it from its organization.
func TestCreate_ForbiddenLookupSaysReadsNeedTheScopeToo(t *testing.T) {
	f := tagsettingstest.New(t)
	f.DenyTenantRoute = true
	r := configured(t, f)

	resp := doCreate(t, r, createPlan(t, unknown, null, "env", "#d73a49"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused lookup must be an error")
	}
	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"`organizations:read`", "refresh", "will not make refresh work"} {
		if !strings.Contains(text, want) {
			t.Errorf("the error must say %q, got:\n%s", want, text)
		}
	}
	if len(f.Only("POST")) != 0 {
		t.Error("nothing may be written")
	}
}

// --- plan ---

type planCase struct {
	prior, planned, config tftypes.Value
}

func doModifyPlan(t *testing.T, r *tagColorResource, pc planCase) *resource.ModifyPlanResponse {
	t.Helper()
	s := tcSchema(t)
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: pc.planned}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: s, Raw: pc.prior},
		Plan:   tfsdk.Plan{Schema: s, Raw: pc.planned},
		Config: tfsdk.Config{Schema: s, Raw: pc.config},
	}, resp)
	return resp
}

func plannedOrg(t *testing.T, resp *resource.ModifyPlanResponse) types.String {
	t.Helper()
	var v types.String
	resp.Plan.GetAttribute(context.Background(), path.Root("organization_id"), &v)
	return v
}

func cfg(t *testing.T, org tftypes.Value) tftypes.Value {
	return obj(t, map[string]tftypes.Value{"organization_id": org, "key": str("env"), "color": str("#d73a49")})
}

func TestModifyPlan_CreateNamesTheResolvedOrganization(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	nullObj := tftypes.NewValue(tcType(t), nil)

	resp := doModifyPlan(t, r, planCase{nullObj, createPlan(t, unknown, null, "env", "#d73a49"), cfg(t, null)})
	mustOK(t, "plan", resp.Diagnostics)
	if got := plannedOrg(t, resp); got.ValueString() != f.OrgID {
		t.Errorf("planned organization_id = %v, want the tenant's owning organization %s", got, f.OrgID)
	}
	if gets := f.Only("GET"); len(gets) != 1 || gets[0].Path != "/v1/tenants/"+f.TenantID+"/tag-colors" {
		t.Errorf("want the tenant route read once, got %+v", f.Requests())
	}

	// The create then writes to exactly the organization the plan named.
	f.Reset()
	created := doCreate(t, r, resp.Plan.Raw)
	mustOK(t, "create", created.Diagnostics)
	if posts := f.Only("POST"); len(posts) != 1 || posts[0].Path != "/v1/organizations/"+f.OrgID+"/tag-colors" {
		t.Errorf("the create must write to the planned organization, got %+v", f.Requests())
	}
	if n := len(f.Only("GET")); n != 0 {
		t.Errorf("the planned organization is known, so the create must not resolve again (%d GETs)", n)
	}
}

func TestModifyPlan_CreateWithAnUnresolvableOrganizationFailsThePlan(t *testing.T) {
	f := tagsettingstest.New(t)
	f.ReportOrganization = false
	r := configured(t, f)
	nullObj := tftypes.NewValue(tcType(t), nil)

	resp := doModifyPlan(t, r, planCase{nullObj, createPlan(t, unknown, null, "env", "#d73a49"), cfg(t, null)})
	if !resp.Diagnostics.HasError() || !strings.Contains(diagsText(resp.Diagnostics), "organization_id") {
		t.Fatalf("a create whose organization cannot be resolved must fail at plan, got:\n%s", diagsText(resp.Diagnostics))
	}
}

func TestModifyPlan_ConfiguredOrganizationIsLeftAloneAndNotLookedUp(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	nullObj := tftypes.NewValue(tcType(t), nil)

	resp := doModifyPlan(t, r, planCase{nullObj, createPlan(t, str(f.OrgID), null, "env", "#d73a49"), cfg(t, str(f.OrgID))})
	mustOK(t, "plan", resp.Diagnostics)
	if len(f.Requests()) != 0 {
		t.Errorf("a configured organization needs no lookup, got %+v", f.Requests())
	}
}

func settledState(t *testing.T, org string) tftypes.Value {
	return obj(t, map[string]tftypes.Value{
		"id": str("44444444-4444-4444-8444-444444444444"), "organization_id": str(org),
		"key": str("env"), "value": null, "color": str("#d73a49"),
		"created_at": str("2026-09-13T12:00:01.000000Z"), "updated_at": str("2026-09-13T12:00:01.000000Z"),
	})
}

// The rule stays where state says, but a provider now pointing at a tenant of
// another organization is named in a WARNING — never an error, never a replace.
func TestModifyPlan_ExistingRuleInAnotherOrganizationWarnsNamingBoth(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	const oldOrg = "33333333-3333-4333-8333-333333333333"
	prior := settledState(t, oldOrg)

	resp := doModifyPlan(t, r, planCase{prior, prior, cfg(t, null)})
	if resp.Diagnostics.HasError() {
		t.Fatalf("must not be an error (it would block destroy): %s", diagsText(resp.Diagnostics))
	}
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 {
		t.Fatalf("want one warning, got:\n%s", diagsText(resp.Diagnostics))
	}
	for _, want := range []string{oldOrg, f.OrgID} {
		if !strings.Contains(ws[0].Detail(), want) {
			t.Errorf("the warning must name %s: %s", want, ws[0].Detail())
		}
	}
	if got := plannedOrg(t, resp); got.ValueString() != oldOrg {
		t.Errorf("planned organization_id = %v, want the state's (the rule is not moved)", got)
	}
	if len(resp.RequiresReplace) != 0 {
		t.Errorf("must not replace, got %v", resp.RequiresReplace)
	}
}

func TestModifyPlan_ExistingRuleQuietWhenSameOrgOrLookupFails(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	prior := settledState(t, f.OrgID)
	if resp := doModifyPlan(t, r, planCase{prior, prior, cfg(t, null)}); len(resp.Diagnostics) != 0 {
		t.Errorf("same organization: no diagnostics, got:\n%s", diagsText(resp.Diagnostics))
	}

	g := tagsettingstest.New(t)
	g.NotRouted = true
	r2 := configured(t, g)
	other := settledState(t, "33333333-3333-4333-8333-333333333333")
	if resp := doModifyPlan(t, r2, planCase{other, other, cfg(t, null)}); len(resp.Diagnostics) != 0 {
		t.Errorf("a failed lookup on an existing rule must stay silent, got:\n%s", diagsText(resp.Diagnostics))
	}
}

func TestModifyPlan_DestroyDoesNothing(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	nullObj := tftypes.NewValue(tcType(t), nil)
	resp := doModifyPlan(t, r, planCase{settledState(t, "33333333-3333-4333-8333-333333333333"), nullObj, nullObj})
	if len(resp.Diagnostics) != 0 || len(f.Requests()) != 0 {
		t.Errorf("a destroy plan must neither call out nor diagnose: %s %+v", diagsText(resp.Diagnostics), f.Requests())
	}
}
