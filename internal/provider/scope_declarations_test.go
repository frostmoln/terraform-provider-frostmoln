package provider

import (
	"context"
	"sort"
	"strings"
	"testing"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestScopeDeclarations asserts the machine-readable authoritative-scope
// declaration (internal/scopedecl) against the live schema of every
// registered resource, in both directions wherever the schema can referee:
//
//   - every registered resource has a Declarations entry, and every entry
//     names a registered resource;
//   - every attribute whose change requires a replacement — detected
//     BEHAVIOURALLY, by replaying the attribute's plan modifiers the way
//     planmodifier_order_test.go does, so RequiresReplaceIf* variants cannot
//     hide — is declared Immutable, and every Immutable path replaces;
//   - every Immutable entry resolves a non-empty reason (the work item's
//     "reason recorded next to the marker");
//   - ImmutableWithoutReplace paths exist, are configurable, and replace
//     NOTHING — a plain RequiresReplace there would destroy an object the
//     provider cannot rebuild under the same name;
//   - every Observes path is a Computed-only attribute;
//   - EnactsNone agrees with the schema (no configurable attribute updates
//     in place);
//   - the resource's Description carries its scopedecl.Summary, so the
//     generated page shows the contract.
//
// What the schema cannot referee — WHICH computed attributes are
// platform-mutable enough to deserve an Observes label — stays a declared
// judgement; the test keeps the declared ones honest.
func TestScopeDeclarations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	seen := map[string]bool{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		var mdResp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp)
		var schemaResp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

		typeName := mdResp.TypeName
		seen[typeName] = true

		t.Run(typeName, func(t *testing.T) {
			if len(schemaResp.Schema.Blocks) > 0 {
				t.Fatalf("resource declares schema Blocks, which this test does not walk — extend walkScopeAttributes")
			}

			decl, declared := scopedecl.Declarations[typeName]
			if !declared {
				t.Fatalf("no scopedecl.Declarations entry — declare this resource's authoritative scope there")
			}

			attrs := map[string]scopeAttrInfo{}
			walkScopeAttributes(t, "", schemaResp.Schema.Attributes, func(attrPath string, a schema.Attribute) {
				attrs[attrPath] = scopeAttrInfo{
					configurable: a.IsRequired() || a.IsOptional(),
					computedOnly: a.IsComputed() && !a.IsRequired() && !a.IsOptional(),
					replaces:     scopeReplacesOnChange(t, attrPath, a),
				}
			})

			immutable := map[string]bool{}
			for _, f := range decl.Immutable {
				info, exists := attrs[f.Path]
				if !exists {
					t.Errorf("Immutable lists %q, which is not an attribute of this resource", f.Path)
					continue
				}
				if !info.replaces {
					t.Errorf("Immutable lists %q, but nothing requires replacement on its change — "+
						"if the change is REFUSED rather than replaced, move it to ImmutableWithoutReplace", f.Path)
				}
				if f.Why == "" && decl.ImmutableWhy == "" {
					t.Errorf("Immutable %q has no reason — set its Why or the entry's ImmutableWhy "+
						"(the reason is recorded next to the marker, that is the point)", f.Path)
				}
				immutable[f.Path] = true
			}

			for _, f := range decl.ImmutableWithoutReplace {
				info, exists := attrs[f.Path]
				if !exists {
					t.Errorf("ImmutableWithoutReplace lists %q, which is not an attribute of this resource", f.Path)
					continue
				}
				if !info.configurable {
					t.Errorf("ImmutableWithoutReplace lists %q, which is not configurable", f.Path)
				}
				if info.replaces {
					t.Errorf("ImmutableWithoutReplace lists %q, but its change DOES require replacement — "+
						"it belongs in Immutable", f.Path)
				}
				if f.Why == "" {
					t.Errorf("ImmutableWithoutReplace %q has no Why — the refusal reason is the contract", f.Path)
				}
			}

			for _, f := range decl.EnactsExcept {
				info, exists := attrs[f.Path]
				if !exists {
					t.Errorf("EnactsExcept lists %q, which is not an attribute of this resource", f.Path)
					continue
				}
				if !info.configurable {
					t.Errorf("EnactsExcept lists %q, which is not configurable — the enacts bullet never covered it", f.Path)
				}
				if info.replaces {
					t.Errorf("EnactsExcept lists %q, but its change requires replacement — it belongs in Immutable", f.Path)
				}
				if f.Why == "" {
					t.Errorf("EnactsExcept %q has no Why — say why the enacts bullet does not cover it", f.Path)
				}
			}

			for _, f := range decl.Observes {
				info, exists := attrs[f.Path]
				if !exists {
					t.Errorf("Observes lists %q, which is not an attribute of this resource", f.Path)
					continue
				}
				if !info.computedOnly {
					t.Errorf("Observes lists %q, which is not a Computed-only attribute — "+
						"the label is for platform-side fields, not configurable ones", f.Path)
				}
				if f.Why == "" {
					t.Errorf("Observes %q has no Why — say what the platform may do to it", f.Path)
				}
			}

			enactsInPlace := false
			for attrPath, info := range attrs {
				if info.configurable && !info.replaces && !immutable[attrPath] {
					enactsInPlace = true
				}
				if info.replaces && !immutable[attrPath] {
					t.Errorf("%s forces a replacement on a real value change but is not declared Immutable "+
						"in scopedecl.Declarations — declare it with its reason", attrPath)
				}
			}
			if decl.EnactsNone && enactsInPlace {
				t.Errorf("EnactsNone is set, but configurable attributes update in place — remove it")
			}
			if !decl.EnactsNone && !enactsInPlace {
				t.Errorf("no configurable attribute updates in place — set EnactsNone so the rendered " +
					"contract does not claim one does")
			}

			for _, def := range decl.Defaults {
				if def.Policy == "" {
					t.Errorf("platform default %q has no policy", def.Name)
				}
				if def.Why == "" {
					t.Errorf("platform default %q has no Why — the policy choice needs its reason", def.Name)
				}
			}

			if !strings.Contains(schemaResp.Schema.Description, scopedecl.Summary(typeName)) {
				t.Errorf("resource Description does not render scopedecl.Summary(%q) — append it so "+
					"the generated page carries the contract", typeName)
			}
		})
	}

	for typeName := range scopedecl.Declarations {
		if !seen[typeName] {
			t.Errorf("scopedecl.Declarations lists unknown resource type %q", typeName)
		}
	}
}

// scopeAttrInfo is what the walk learns about one attribute.
type scopeAttrInfo struct {
	configurable bool
	computedOnly bool
	replaces     bool
}

// walkScopeAttributes calls fn for every attribute reachable from attrs,
// recursing into nested attributes with a dotted path. Unlike
// walkOptionalComputed it walks every attribute class: the create-immutable
// set is mostly Required attributes, which the plan-modifier walk never sees.
func walkScopeAttributes(t *testing.T, prefix string, attrs map[string]schema.Attribute, fn func(string, schema.Attribute)) {
	t.Helper()

	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		a := attrs[name]
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}

		switch nested := a.(type) {
		case schema.SingleNestedAttribute:
			if len(nested.PlanModifiers) > 0 {
				t.Fatalf("%s: object-level plan modifiers are not replayed — extend scopeReplacesOnChange", attrPath)
			}
			walkScopeAttributes(t, attrPath, nested.Attributes, fn)
		case schema.ListNestedAttribute:
			if len(nested.PlanModifiers) > 0 {
				t.Fatalf("%s: object-level plan modifiers are not replayed — extend scopeReplacesOnChange", attrPath)
			}
			walkScopeAttributes(t, attrPath, nested.NestedObject.Attributes, fn)
		case schema.SetNestedAttribute:
			if len(nested.PlanModifiers) > 0 {
				t.Fatalf("%s: object-level plan modifiers are not replayed — extend scopeReplacesOnChange", attrPath)
			}
			walkScopeAttributes(t, attrPath, nested.NestedObject.Attributes, fn)
		case schema.MapNestedAttribute:
			if len(nested.PlanModifiers) > 0 {
				t.Fatalf("%s: object-level plan modifiers are not replayed — extend scopeReplacesOnChange", attrPath)
			}
			walkScopeAttributes(t, attrPath, nested.NestedObject.Attributes, fn)
		default:
			fn(attrPath, a)
		}
	}
}

// scopeReplacesOnChange reports whether a real value change on the attribute
// requires a replacement, by replaying its plan modifiers the way the
// framework chains them — the same behavioural detection as
// planmodifier_order_test.go's replayModifiers, with the config value SET to
// the new value so RequiresReplaceIfConfigured and RequiresReplaceIf fire
// exactly as they do on a real plan. %T matching is unsound: RequiresReplace,
// RequiresReplaceIfConfigured and a no-op RequiresReplaceIf share one
// concrete type.
//
// The synthetic state and plan are planmodifier_order_test.go's
// updateState/updatePlan: both RequiresReplace and UseStateForUnknown no-op on
// a null raw state (create) or plan (destroy), and the If-functions in use
// (instance_access, cross_vpc) read only their own attribute's values.
func scopeReplacesOnChange(t *testing.T, attrPath string, a schema.Attribute) bool {
	t.Helper()

	switch a := a.(type) {
	case schema.StringAttribute:
		req := planmodifier.StringRequest{
			Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
			StateValue: types.StringValue("scope-a"), PlanValue: types.StringValue("scope-b"),
			ConfigValue: types.StringValue("scope-b"),
		}
		replace := false
		for _, m := range a.PlanModifiers {
			resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
			m.PlanModifyString(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: plan modifier errored under the synthetic replay — it likely reads sibling attributes this walk does not populate; extend the replay before trusting the replacement verdict", attrPath)
			}
			req.PlanValue = resp.PlanValue
			replace = replace || resp.RequiresReplace
		}
		return replace

	case schema.BoolAttribute:
		req := planmodifier.BoolRequest{
			Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
			StateValue: types.BoolValue(false), PlanValue: types.BoolValue(true),
			ConfigValue: types.BoolValue(true),
		}
		replace := false
		for _, m := range a.PlanModifiers {
			resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
			m.PlanModifyBool(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: plan modifier errored under the synthetic replay — it likely reads sibling attributes this walk does not populate; extend the replay before trusting the replacement verdict", attrPath)
			}
			req.PlanValue = resp.PlanValue
			replace = replace || resp.RequiresReplace
		}
		return replace

	case schema.Int64Attribute:
		req := planmodifier.Int64Request{
			Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
			StateValue: types.Int64Value(1), PlanValue: types.Int64Value(2),
			ConfigValue: types.Int64Value(2),
		}
		replace := false
		for _, m := range a.PlanModifiers {
			resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
			m.PlanModifyInt64(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: plan modifier errored under the synthetic replay — it likely reads sibling attributes this walk does not populate; extend the replay before trusting the replacement verdict", attrPath)
			}
			req.PlanValue = resp.PlanValue
			replace = replace || resp.RequiresReplace
		}
		return replace

	case schema.ListAttribute:
		req := planmodifier.ListRequest{
			Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
			StateValue: types.ListValueMust(a.ElementType, []attr.Value{scopeElementValue(t, attrPath, a.ElementType, "a")}),
			PlanValue:  types.ListValueMust(a.ElementType, []attr.Value{scopeElementValue(t, attrPath, a.ElementType, "b")}),
		}
		req.ConfigValue = req.PlanValue
		replace := false
		for _, m := range a.PlanModifiers {
			resp := &planmodifier.ListResponse{PlanValue: req.PlanValue}
			m.PlanModifyList(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: plan modifier errored under the synthetic replay — it likely reads sibling attributes this walk does not populate; extend the replay before trusting the replacement verdict", attrPath)
			}
			req.PlanValue = resp.PlanValue
			replace = replace || resp.RequiresReplace
		}
		return replace

	case schema.SetAttribute:
		req := planmodifier.SetRequest{
			Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
			StateValue: types.SetValueMust(a.ElementType, []attr.Value{scopeElementValue(t, attrPath, a.ElementType, "a")}),
			PlanValue:  types.SetValueMust(a.ElementType, []attr.Value{scopeElementValue(t, attrPath, a.ElementType, "b")}),
		}
		req.ConfigValue = req.PlanValue
		replace := false
		for _, m := range a.PlanModifiers {
			resp := &planmodifier.SetResponse{PlanValue: req.PlanValue}
			m.PlanModifySet(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: plan modifier errored under the synthetic replay — it likely reads sibling attributes this walk does not populate; extend the replay before trusting the replacement verdict", attrPath)
			}
			req.PlanValue = resp.PlanValue
			replace = replace || resp.RequiresReplace
		}
		return replace

	case schema.MapAttribute:
		req := planmodifier.MapRequest{
			Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
			StateValue: types.MapValueMust(a.ElementType, map[string]attr.Value{"k": scopeElementValue(t, attrPath, a.ElementType, "a")}),
			PlanValue:  types.MapValueMust(a.ElementType, map[string]attr.Value{"k": scopeElementValue(t, attrPath, a.ElementType, "b")}),
		}
		req.ConfigValue = req.PlanValue
		replace := false
		for _, m := range a.PlanModifiers {
			resp := &planmodifier.MapResponse{PlanValue: req.PlanValue}
			m.PlanModifyMap(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: plan modifier errored under the synthetic replay — it likely reads sibling attributes this walk does not populate; extend the replay before trusting the replacement verdict", attrPath)
			}
			req.PlanValue = resp.PlanValue
			replace = replace || resp.RequiresReplace
		}
		return replace

	default:
		t.Fatalf("%s: unsupported attribute type %T — extend scopeReplacesOnChange", attrPath, a)
		return false
	}
}

func scopeElementValue(t *testing.T, attrPath string, elemType attr.Type, seed string) attr.Value {
	t.Helper()
	switch elemType {
	case types.StringType:
		return types.StringValue("scope-" + seed)
	case types.Int64Type:
		if seed == "a" {
			return types.Int64Value(1)
		}
		return types.Int64Value(2)
	default:
		t.Fatalf("%s: unsupported collection element type %s — extend scopeElementValue", attrPath, elemType)
		return nil
	}
}
