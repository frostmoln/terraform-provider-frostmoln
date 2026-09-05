package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// A write-only attribute `<base>_wo` comes as a triple: the legacy attribute
// `<base>` it replaces, and the practitioner-set change tracker
// `<base>_wo_version` that carries the change detection the write-only value
// cannot. The whole contract below is about the three of them together.
const (
	woSuffix      = "_wo"
	versionSuffix = "_wo_version"
)

// writeOnlyAttributes names, per resource type, every WriteOnly attribute the
// walk is expected to find. It is checked in both directions — an attribute
// here that the walk misses fails, and one the walk finds that is not listed
// fails — so a refactor that quietly stopped covering a resource cannot leave
// the suite green. It doubles as the running checklist for the staged rollout.
//
// Adding a row is the point at which to re-check the two things this test
// CANNOT see, because both are per-resource facts rather than schema shape:
//
//  1. Whether the resource's API returns the protected material on GET. If it
//     does, `fromAPI` must not adopt it, or refresh writes it back into state
//     and the feature is defeated on the first plan.
//  2. Whether Update reads the write-only value through the SAME reader Create
//     uses. checkUnknownFailsClosed drives Create only; a second, separate
//     reader in Update would ship unguarded with this suite green.
var writeOnlyAttributes = map[string][]string{
	"frostmoln_secret":            {"secret_value_wo"},
	"frostmoln_instance":          {"console_password_wo", "user_data_wo"},
	"frostmoln_launch_template":   {"user_data_wo"},
	"frostmoln_appgw_certificate": {"private_key_pem_wo"},
	// (1) The caches API returns NO credential — no endpoint reads an upstream
	// credential back — so fromAPI cannot adopt one; it writes neither the
	// legacy password nor the write-only pair. (2) There is no Update: every
	// configurable attribute is RequiresReplace, so Create is the only reader
	// and there is no second one to drift from it.
	"frostmoln_container_registry_cache": {"password_wo"},
}

// TestWriteOnlyAttributeContract asserts, for every WriteOnly attribute of every
// resource, the invariants that make the write-only path safe. Each of them has
// already been violated or nearly violated at least once while the stages were
// built one resource at a time, which is the reason this lives at provider level
// rather than being copied a fourth time.
//
// Everything that can be checked by behaviour is checked by behaviour: the
// validators are RUN against a real config rather than matched on their type.
// Type matching is unsound here for the same reason it is unsound in
// planmodifier_order_test.go — stringvalidator.AlsoRequires, ConflictsWith and
// ExactlyOneOf are unexported types, and mutual exclusion is legitimately
// spelled either as ConflictsWith on the attribute or as a resource-level
// ExactlyOneOf where the legacy attribute used to be Required. Running them
// makes the two spellings indistinguishable, which is what the contract wants.
//
// Out of scope on purpose: the legacy twin's plan modifiers. They differ
// legitimately per resource — instance.user_data carries RequiresReplace,
// secret.secret_value must carry none — so that invariant stays pinned
// per-resource, in secret's TestSchemaWriteOnlyAttributes, which secret's own
// read-path guard names as its pin.
func TestWriteOnlyAttributeContract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	seen := map[string][]string{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		var mdResp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp)
		var schemaResp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
		s := schemaResp.Schema

		names := writeOnlyAttributeNames(t, mdResp.TypeName, s)
		if len(names) > 0 {
			seen[mdResp.TypeName] = names
		}
		for _, name := range names {
			t.Run(mdResp.TypeName+"."+name, func(t *testing.T) {
				checkWriteOnlyTriple(ctx, t, r, s, name)
				checkUnknownFailsClosed(ctx, t, newResource, s, name)
				checkNeitherFormAgreesWithExactlyOneOf(ctx, t, newResource, r, s, name)
			})
		}
	}

	// Both directions, so neither the table nor the walk can drift silently.
	for typeName, want := range writeOnlyAttributes {
		got, walked := seen[typeName]
		if !walked {
			t.Errorf("%s: listed in writeOnlyAttributes but no WriteOnly attribute was found on it", typeName)
			continue
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: found WriteOnly attributes %v, expected %v — update writeOnlyAttributes", typeName, got, want)
		}
	}
	for typeName := range seen {
		if _, listed := writeOnlyAttributes[typeName]; !listed {
			t.Errorf("%s: has a WriteOnly attribute but is not listed in writeOnlyAttributes — add it there, "+
				"and check the two per-resource facts named on that table", typeName)
		}
	}
}

// writeOnlyAttributeNames returns the sorted names of the schema's root
// WriteOnly attributes, failing loudly on any shape the contract does not model
// rather than passing over it.
func writeOnlyAttributeNames(t *testing.T, typeName string, s schema.Schema) []string {
	t.Helper()

	// Blocks carry their own attributes and are not walked. The provider uses
	// none — planmodifier_order_test.go enforces that too — but this test must
	// not depend on that one running.
	if len(s.Blocks) > 0 {
		t.Fatalf("%s: declares schema Blocks, which this contract does not walk — extend it", typeName)
	}

	var names []string
	for name, a := range s.Attributes {
		if a.IsWriteOnly() {
			names = append(names, name)
		}
		assertNoNestedWriteOnly(t, name, a)
	}
	sort.Strings(names)
	return names
}

// assertNoNestedWriteOnly recurses through nested attributes to any depth. A
// WriteOnly attribute below the root would need a path-aware rewrite of the
// config building below, so it must fail rather than pass unchecked.
func assertNoNestedWriteOnly(t *testing.T, prefix string, a schema.Attribute) {
	t.Helper()

	var children map[string]schema.Attribute
	switch nested := a.(type) {
	case schema.SingleNestedAttribute:
		children = nested.Attributes
	case schema.ListNestedAttribute:
		children = nested.NestedObject.Attributes
	case schema.SetNestedAttribute:
		children = nested.NestedObject.Attributes
	case schema.MapNestedAttribute:
		children = nested.NestedObject.Attributes
	default:
		return
	}

	for name, child := range children {
		attrPath := prefix + "." + name
		if child.IsWriteOnly() {
			t.Fatalf("%s: a nested WriteOnly attribute is not covered by this contract — extend it", attrPath)
		}
		assertNoNestedWriteOnly(t, attrPath, child)
	}
}

// checkWriteOnlyTriple runs the schema half of the contract for one write-only
// attribute and the two attributes it is paired with.
func checkWriteOnlyTriple(ctx context.Context, t *testing.T, r resource.Resource, s schema.Schema, woName string) {
	t.Helper()

	if !strings.HasSuffix(woName, woSuffix) {
		t.Fatalf("%s: a WriteOnly attribute must be named <legacy>%s so its legacy twin and version companion are derivable", woName, woSuffix)
	}
	legacyName := strings.TrimSuffix(woName, woSuffix)
	versionName := legacyName + versionSuffix

	wo := stringAttribute(t, s, woName)
	legacy := stringAttribute(t, s, legacyName)
	version := stringAttribute(t, s, versionName)

	// The write-only attribute itself. Optional-only: the framework rejects
	// WriteOnly+Computed but permits WriteOnly+Required, and a Required one
	// makes the legacy twin unreachable and forces every existing config onto
	// Terraform 1.11.
	if !wo.IsOptional() || wo.IsComputed() {
		t.Errorf("%s: a WriteOnly attribute must be Optional and not Computed", woName)
	}
	if !wo.IsSensitive() {
		t.Errorf("%s: a WriteOnly attribute must be Sensitive", woName)
	}
	if len(wo.PlanModifiers) != 0 {
		t.Errorf("%s: a WriteOnly attribute must have zero plan modifiers — it is null in prior state, plan and "+
			"final state, so a modifier compares null against null and can never fire", woName)
	}

	// The legacy twin it replaces. Optional-only: making it Computed would let
	// an API-normalised value be written back over the configured one, which
	// Terraform rejects as an inconsistent result.
	if !legacy.IsOptional() || legacy.IsComputed() {
		t.Errorf("%s: the legacy twin of a WriteOnly attribute must be Optional and not Computed", legacyName)
	}
	if !legacy.IsSensitive() {
		t.Errorf("%s: the legacy twin still lands in state in plaintext and must be Sensitive", legacyName)
	}

	// The version companion. NOT Sensitive on purpose: it is the signal telling
	// a practitioner why an update is happening, and the documented argument
	// against deriving it from the protected material depends on it being
	// readable in plan output.
	if !version.IsOptional() || version.IsComputed() {
		t.Errorf("%s: the version companion must be Optional and not Computed — the practitioner sets it", versionName)
	}
	if version.IsWriteOnly() {
		t.Errorf("%s: the version companion must be stored in state; it carries the change detection the "+
			"write-only value cannot", versionName)
	}
	if version.IsSensitive() {
		t.Errorf("%s: the version companion must NOT be Sensitive — a practitioner has to be able to read it "+
			"in plan output to see why an update is happening", versionName)
	}

	// Behavioural half. Each case names the config it validates and what the
	// contract requires of it.
	set := func(pairs ...string) map[string]types.String {
		out := map[string]types.String{}
		for i := 0; i < len(pairs); i += 2 {
			out[pairs[i]] = types.StringValue(pairs[i+1])
		}
		return out
	}
	validate := func(values map[string]types.String) diag.Diagnostics {
		cfg := configWith(ctx, t, s, values)
		var diags diag.Diagnostics
		for _, name := range []string{woName, legacyName, versionName} {
			a := stringAttribute(t, s, name)
			v, found := values[name]
			if !found {
				v = types.StringNull()
			}
			diags.Append(runStringValidators(ctx, a, cfg, path.Root(name), v)...)
		}
		diags.Append(runConfigValidators(ctx, r, cfg)...)
		return diags
	}

	// The baseline. Everything else is a departure from it, so an error here
	// would make the negative cases below prove nothing.
	if d := validate(set(woName, "x", versionName, "1")); d.HasError() {
		t.Fatalf("%s: the write-only attribute set together with its version companion must validate cleanly, got %v", woName, d.Errors())
	}
	if d := validate(set(woName, "", versionName, "1")); !d.HasError() {
		t.Errorf("%s: an empty value must be rejected (LengthAtLeast(1)) — \"\" is not null, so it passes every "+
			"null guard and reaches the wire, and the write-only path removes the plan line that would show it", woName)
	}
	if d := validate(set(woName, "x")); !d.HasError() {
		t.Errorf("%s: must AlsoRequires(%s) — a document with no version is never sent, on a green apply", woName, versionName)
	}
	// The legacy twin is set here on purpose, so a resource-level ExactlyOneOf
	// is SATISFIED and only the companion's own back-link can refuse this
	// config. Leaving the pair unset instead makes this assertion pass on the
	// strength of ExactlyOneOf alone, and the back-link could then be deleted
	// with the suite green — which it was, until a reviewer caught it.
	if d := validate(set(legacyName, "y", versionName, "1")); !d.HasError() {
		t.Errorf("%s: must AlsoRequires(%s) BACK — a version with no document validates clean, the create sends "+
			"nothing, and the practitioner gets a silent no-op", versionName, woName)
	}
	if d := validate(set(woName, "x", versionName, "1", legacyName, "y")); !d.HasError() {
		t.Errorf("%s: must be mutually exclusive with %s, by ConflictsWith or a resource-level ExactlyOneOf", woName, legacyName)
	}
	if d := validate(set(legacyName, "y")); len(d.Warnings()) == 0 {
		t.Errorf("%s: must carry PreferWriteOnlyAttribute(%s) so a practitioner on the legacy attribute is told "+
			"there is a form that stays out of state", legacyName, woName)
	}
}

// checkUnknownFailsClosed drives the resource's Create with the write-only
// attribute unknown in config and asserts it refuses.
//
// Config is fully resolved by apply, so this should be unreachable — but the
// alternative spelling (treating unknown as "no value" in the caller's guard)
// drops the value silently and reports success, which is the one outcome the
// write-only path must never produce. The check is behavioural because the guard
// is in the resource's config reader, not in its schema.
//
// Only Create is driven. That is sufficient only while Update reads the value
// through the same reader, which is a per-resource fact this test cannot see —
// see the note on writeOnlyAttributes.
func checkUnknownFailsClosed(ctx context.Context, t *testing.T, newResource func() resource.Resource, s schema.Schema, woName string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s: an unknown write-only value must stop before the platform is called, got %s %s", woName, r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newResource()
	rc, ok := r.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatalf("%s: resource is not configurable, so Create cannot be driven — extend this contract", woName)
	}
	var cfgResp resource.ConfigureResponse
	rc.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &cfgResp)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("Configure failed: %v", cfgResp.Diagnostics.Errors())
	}

	// The plan is all-null: a write-only attribute is null in the plan by
	// construction, so the value can only ever reach the provider through the
	// config, which is what the guard reads.
	plan := tfsdk.Plan{Schema: s, Raw: nullObject(ctx, t, s)}
	cfg := configWith(ctx, t, s, map[string]types.String{woName: types.StringUnknown()})

	createResp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: nullObject(ctx, t, s)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan, Config: cfg}, &createResp)

	// The summary is matched exactly rather than by substring: "user_data_wo"
	// is a substring of "user_data_wo_version", so a looser match would accept
	// an unrelated diagnostic about the companion. Matching exactly also keeps
	// the wording identical across resources, which is how a practitioner
	// recognises the failure.
	want := woName + " Is Unknown At Apply"
	if !hasErrorSummary(createResp.Diagnostics, want) {
		t.Errorf("%s: an unknown write-only value must fail the apply with the diagnostic %q; the read has to "+
			"fail CLOSED, not fall through to \"no value supplied\". Got: %v",
			woName, want, createResp.Diagnostics.Errors())
	}
}

// checkNeitherFormAgreesWithExactlyOneOf ties the two halves of the
// "a value is required" rule together, so neither can drift from the other.
//
// The rule is spelled twice by necessity, in two layers that cannot see each
// other: as a resource-level ExactlyOneOf, which refuses a config supplying
// neither form at PLAN time; and as writeonly.Attr.ExactlyOne, which refuses the
// same config at APPLY time, where the plan-time validator is skipped because it
// bails on an unknown path. Set one and forget the other and the floor is
// silently missing on exactly the configurations that need it — a resource whose
// legacy attribute was relaxed from Required would newly accept a config
// supplying no value at all.
//
// Rather than reach into either package's unexported state, this drives both
// layers with the same config and asserts they agree.
func checkNeitherFormAgreesWithExactlyOneOf(ctx context.Context, t *testing.T, newResource func() resource.Resource, r resource.Resource, s schema.Schema, woName string) {
	t.Helper()

	legacyName := strings.TrimSuffix(woName, woSuffix)

	// Plan-time: does a resource-level validator refuse "neither form set"?
	empty := configWith(ctx, t, s, map[string]types.String{})
	planRefuses := runConfigValidators(ctx, r, empty).HasError()

	// Apply-time: does Create refuse the same config? Any request reaching the
	// server means it did not.
	reached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	res := newResource()
	rc, ok := res.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatalf("%s: resource is not configurable", woName)
	}
	var cfgResp resource.ConfigureResponse
	rc.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &cfgResp)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("Configure failed: %v", cfgResp.Diagnostics.Errors())
	}

	createResp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: nullObject(ctx, t, s)}}
	res.Create(ctx, resource.CreateRequest{
		Plan:   tfsdk.Plan{Schema: s, Raw: nullObject(ctx, t, s)},
		Config: empty,
	}, &createResp)
	applyRefuses := createResp.Diagnostics.HasError() && !reached

	if planRefuses != applyRefuses {
		t.Errorf("%s: a config setting neither %s nor %s is refused at plan time (%v) but at apply "+
			"time (%v) — the two spellings of the rule disagree. A resource-level ExactlyOneOf "+
			"needs writeonly.Attr.ExactlyOne set to match, and vice versa: the plan-time validator "+
			"is SKIPPED whenever a path is unknown, which is when the apply-time half is the only "+
			"thing holding the floor", woName, legacyName, woName, planRefuses, applyRefuses)
	}
}

func hasErrorSummary(diags diag.Diagnostics, summary string) bool {
	for _, d := range diags.Errors() {
		if d.Summary() == summary {
			return true
		}
	}
	return false
}

func stringAttribute(t *testing.T, s schema.Schema, name string) schema.StringAttribute {
	t.Helper()
	a, found := s.Attributes[name]
	if !found {
		t.Fatalf("%s: expected attribute is missing. A WriteOnly attribute must come as the triple "+
			"<legacy>/<legacy>%s/<legacy>%s; a write-only attribute with no legacy twin is a shape this "+
			"contract does not model yet", name, woSuffix, versionSuffix)
	}
	sa, ok := a.(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s: expected a StringAttribute, got %T — extend this contract", name, a)
	}
	return sa
}

func runStringValidators(ctx context.Context, a schema.StringAttribute, cfg tfsdk.Config, p path.Path, v types.String) diag.Diagnostics {
	req := validator.StringRequest{
		Path: p, PathExpression: p.Expression(), Config: cfg, ConfigValue: v,
		// PreferWriteOnlyAttribute stays silent unless the client can actually
		// use a write-only attribute, so without this the warning it exists to
		// emit never appears and the assertion on it would be vacuous.
		ClientCapabilities: validator.ValidateSchemaClientCapabilities{WriteOnlyAttributesAllowed: true},
	}
	var out diag.Diagnostics
	for _, val := range a.Validators {
		resp := &validator.StringResponse{}
		val.ValidateString(ctx, req, resp)
		out.Append(resp.Diagnostics...)
	}
	return out
}

func runConfigValidators(ctx context.Context, r resource.Resource, cfg tfsdk.Config) diag.Diagnostics {
	var out diag.Diagnostics
	cv, ok := r.(resource.ResourceWithConfigValidators)
	if !ok {
		return out
	}
	for _, v := range cv.ConfigValidators(ctx) {
		resp := &resource.ValidateConfigResponse{}
		v.ValidateResource(ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
		out.Append(resp.Diagnostics...)
	}
	return out
}

// configWith builds a config of the resource's own schema in which every
// attribute is null except the named ones. Building it from the schema rather
// than from a hand-written object keeps the path-based validators (AlsoRequires,
// ConflictsWith, ExactlyOneOf) able to resolve the paths they check.
func configWith(ctx context.Context, t *testing.T, s schema.Schema, values map[string]types.String) tfsdk.Config {
	t.Helper()

	objType := schemaObjectType(ctx, t, s)
	attrs := map[string]tftypes.Value{}
	for name, attrType := range objType.AttributeTypes {
		v, override := values[name]
		switch {
		case !override:
			attrs[name] = tftypes.NewValue(attrType, nil)
		case v.IsUnknown():
			attrs[name] = tftypes.NewValue(attrType, tftypes.UnknownValue)
		default:
			attrs[name] = tftypes.NewValue(attrType, v.ValueString())
		}
	}
	for name := range values {
		if _, found := objType.AttributeTypes[name]; !found {
			t.Fatalf("%s: not an attribute of this resource", name)
		}
	}
	return tfsdk.Config{Schema: s, Raw: tftypes.NewValue(objType, attrs)}
}

func nullObject(ctx context.Context, t *testing.T, s schema.Schema) tftypes.Value {
	t.Helper()

	objType := schemaObjectType(ctx, t, s)
	attrs := map[string]tftypes.Value{}
	for name, attrType := range objType.AttributeTypes {
		attrs[name] = tftypes.NewValue(attrType, nil)
	}
	return tftypes.NewValue(objType, attrs)
}

func schemaObjectType(ctx context.Context, t *testing.T, s schema.Schema) tftypes.Object {
	t.Helper()

	objType, ok := s.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("resource schema did not render as a tftypes.Object")
	}
	return objType
}
