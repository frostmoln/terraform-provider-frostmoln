package provider

import (
	"context"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// mustReplaceOnRealChange lists, per resource type, every Optional+Computed
// attribute that is create-only, i.e. whose value change has to force a
// replacement. It guards the direction the unknown-value invariant cannot: that
// an attribute's RequiresReplace() was not simply deleted. Paths are dotted for
// attributes inside a nested attribute.
//
// The table is checked in both directions — an entry that no longer replaces
// fails, and an attribute that replaces without an entry fails — so it cannot
// drift out of date silently.
//
// Only Optional+Computed attributes appear here: a Required or Optional-only
// attribute is never marked unknown from a null config, so it cannot hit the
// ordering trap and is out of the walk's scope.
var mustReplaceOnRealChange = map[string][]string{
	"frostmoln_api_key":               {"expires_at"},
	"frostmoln_apache_instance":       {"php_enabled", "php_version"},
	"frostmoln_bucket":                {"region", "storage_class"},
	"frostmoln_instance":              {"zone"},
	"frostmoln_kubernetes_cluster":    {"version", "control_plane_tier", "region", "scheme", "addons", "initial_node_pool.name"},
	"frostmoln_kubernetes_node_pool":  {"name"},
	"frostmoln_load_balancer":         {"scheme", "provider_type"},
	"frostmoln_messaging_instance":    {"engine", "version"},
	"frostmoln_mysql_backup":          {"type"},
	"frostmoln_mysql_instance":        {"ha_enabled"},
	"frostmoln_mysql_read_replica":    {"flavor_id"},
	"frostmoln_nginx_instance":        {"php_enabled", "php_version"},
	"frostmoln_postgres_backup":       {"type"},
	"frostmoln_postgres_instance":     {"ha_enabled"},
	"frostmoln_postgres_read_replica": {"flavor_id"},
	"frostmoln_subnet":                {"zone", "gateway_ip", "dns_servers"},
	"frostmoln_volume":                {"volume_type", "zone", "encrypted"},
	"frostmoln_volume_attachment":     {"device_path"},
	"frostmoln_webserver_domain":      {"tls_enabled", "is_default"},
}

// TestOptionalComputedAttributesReplaceOnlyOnRealChange asserts, for every
// Optional+Computed attribute of every resource, that an update plan which
// leaves the attribute unknown (the practitioner omitted it from config, so the
// framework marks it unknown — fwserver.MarkComputedNilsAsUnknown) does NOT
// force a replacement, while a real value change still does.
//
// This is a behavioural check on purpose. Attribute plan modifiers run in slice
// order and chain their PlanValue, with RequiresReplace sticky once set, so
// RequiresReplace() placed BEFORE UseStateForUnknown() compares the unknown
// against the state value, they differ, and every unrelated change plans a
// destroy/recreate. Asserting the modifiers are merely present cannot see that,
// and matching on a modifier's %T is unsound: RequiresReplace,
// RequiresReplaceIfConfigured and a no-op RequiresReplaceIf all render as
// requiresReplaceIfModifier.
func TestOptionalComputedAttributesReplaceOnlyOnRealChange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	seen := map[string]map[string]bool{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		var mdResp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp)
		var schemaResp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

		typeName := mdResp.TypeName
		seen[typeName] = map[string]bool{}

		t.Run(typeName, func(t *testing.T) {
			// Blocks carry their own attributes and are not walked below. The
			// provider uses none; fail loudly rather than skip them silently the
			// day one is added.
			if len(schemaResp.Schema.Blocks) > 0 {
				t.Fatalf("resource declares schema Blocks, which this test does not walk — extend walkOptionalComputed")
			}

			listed := map[string]bool{}
			for _, attrPath := range mustReplaceOnRealChange[typeName] {
				listed[attrPath] = true
			}

			walkOptionalComputed(t, "", schemaResp.Schema.Attributes, func(attrPath string, a schema.Attribute) {
				replacesOnChange, replacesOnUnknown := replayModifiers(t, attrPath, a)
				seen[typeName][attrPath] = replacesOnChange

				// MarkComputedNilsAsUnknown returns the value untouched for an
				// attribute that declares a schema default (its
				// fwschema.AttributeWith<Type>DefaultValue case), so a defaulted
				// attribute is never handed an unknown plan value from a null
				// config and the ordering trap cannot reach it. It can still be
				// unknown from an unknown *config* expression, but no modifier
				// ordering helps there — UseStateForUnknown bails on an unknown
				// config value by design.
				if replacesOnUnknown && !hasDefault(a) {
					t.Errorf("%s: an unknown plan value against an unchanged state must NOT require replacement — "+
						"RequiresReplace() has to be ordered after UseStateForUnknown()", attrPath)
				}
				if replacesOnChange && !listed[attrPath] {
					t.Errorf("%s: forces a replacement on a real value change but is not listed in "+
						"mustReplaceOnRealChange — add it there so the RequiresReplace cannot be dropped unnoticed", attrPath)
				}
			})

			for _, attrPath := range mustReplaceOnRealChange[typeName] {
				replaces, walked := seen[typeName][attrPath]
				if !walked {
					t.Errorf("%s: listed in mustReplaceOnRealChange but is not an Optional+Computed attribute of this resource", attrPath)
					continue
				}
				if !replaces {
					t.Errorf("%s: a real value change must still require replacement", attrPath)
				}
			}
		})
	}

	// A resource type in the table that no longer exists would otherwise let its
	// expectations silently stop being checked.
	for typeName := range mustReplaceOnRealChange {
		if _, found := seen[typeName]; !found {
			t.Errorf("mustReplaceOnRealChange lists unknown resource type %q", typeName)
		}
	}
}

// walkOptionalComputed calls fn for every Optional+Computed attribute reachable
// from attrs, recursing into nested attributes with a dotted path.
func walkOptionalComputed(t *testing.T, prefix string, attrs map[string]schema.Attribute, fn func(string, schema.Attribute)) {
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
			walkOptionalComputed(t, attrPath, nested.Attributes, fn)
		case schema.ListNestedAttribute, schema.SetNestedAttribute, schema.MapNestedAttribute:
			// Their children would be skipped silently, quietly retiring the
			// invariant for them. The provider has none today.
			t.Fatalf("%s: nested-collection attribute (%T) is not walked — extend walkOptionalComputed", attrPath, nested)
		}
		if a.IsOptional() && a.IsComputed() {
			fn(attrPath, a)
		}
	}
}

// hasDefault reports whether the attribute declares a schema-level default.
func hasDefault(a schema.Attribute) bool {
	switch a := a.(type) {
	case schema.StringAttribute:
		return a.Default != nil
	case schema.BoolAttribute:
		return a.Default != nil
	case schema.Int64Attribute:
		return a.Default != nil
	case schema.ListAttribute:
		return a.Default != nil
	case schema.SetAttribute:
		return a.Default != nil
	case schema.MapAttribute:
		return a.Default != nil
	case schema.SingleNestedAttribute:
		return a.Default != nil
	default:
		return false
	}
}

// replayModifiers runs the attribute's plan modifiers twice the way the
// framework chains them (fwserver/attribute_plan_modification.go): each modifier
// sees the previous one's PlanValue, and RequiresReplace, once set, is never
// unset. It reports whether a real value change requires a replacement, and
// whether an unknown plan value against an unchanged state does.
//
// The config value is null throughout — that is exactly the unconfigured case
// this guards, and it is what makes the framework mark the plan value unknown.
//
// The state and plan are synthetic (see updateState/updatePlan): the two
// modifiers only ask whether their Raw is null. The equivalent replay against a
// realistically built tfsdk.State/tfsdk.Plan lives in
// internal/resource/{nginx,apache}_instance/resource_test.go — keep those as the
// cross-check that this shortcut still agrees with the real construction.
func replayModifiers(t *testing.T, attrPath string, a schema.Attribute) (onChange, onUnknown bool) {
	t.Helper()

	switch a := a.(type) {
	case schema.StringAttribute:
		state, changed := types.StringValue("planmodifier-a"), types.StringValue("planmodifier-b")
		run := func(planValue types.String) bool {
			req := planmodifier.StringRequest{
				Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
				StateValue: state, PlanValue: planValue, ConfigValue: types.StringNull(),
			}
			replace := false
			for _, m := range a.PlanModifiers {
				resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
				m.PlanModifyString(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
				replace = replace || resp.RequiresReplace
			}
			return replace
		}
		return run(changed), run(types.StringUnknown())

	case schema.BoolAttribute:
		state, changed := types.BoolValue(false), types.BoolValue(true)
		run := func(planValue types.Bool) bool {
			req := planmodifier.BoolRequest{
				Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
				StateValue: state, PlanValue: planValue, ConfigValue: types.BoolNull(),
			}
			replace := false
			for _, m := range a.PlanModifiers {
				resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
				m.PlanModifyBool(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
				replace = replace || resp.RequiresReplace
			}
			return replace
		}
		return run(changed), run(types.BoolUnknown())

	case schema.Int64Attribute:
		state, changed := types.Int64Value(1), types.Int64Value(2)
		run := func(planValue types.Int64) bool {
			req := planmodifier.Int64Request{
				Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
				StateValue: state, PlanValue: planValue, ConfigValue: types.Int64Null(),
			}
			replace := false
			for _, m := range a.PlanModifiers {
				resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
				m.PlanModifyInt64(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
				replace = replace || resp.RequiresReplace
			}
			return replace
		}
		return run(changed), run(types.Int64Unknown())

	case schema.ListAttribute:
		state := types.ListValueMust(a.ElementType, []attr.Value{elementValue(t, attrPath, a.ElementType, "a")})
		changed := types.ListValueMust(a.ElementType, []attr.Value{elementValue(t, attrPath, a.ElementType, "b")})
		run := func(planValue types.List) bool {
			req := planmodifier.ListRequest{
				Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
				StateValue: state, PlanValue: planValue, ConfigValue: types.ListNull(a.ElementType),
			}
			replace := false
			for _, m := range a.PlanModifiers {
				resp := &planmodifier.ListResponse{PlanValue: req.PlanValue}
				m.PlanModifyList(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
				replace = replace || resp.RequiresReplace
			}
			return replace
		}
		return run(changed), run(types.ListUnknown(a.ElementType))

	case schema.SetAttribute:
		state := types.SetValueMust(a.ElementType, []attr.Value{elementValue(t, attrPath, a.ElementType, "a")})
		changed := types.SetValueMust(a.ElementType, []attr.Value{elementValue(t, attrPath, a.ElementType, "b")})
		run := func(planValue types.Set) bool {
			req := planmodifier.SetRequest{
				Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
				StateValue: state, PlanValue: planValue, ConfigValue: types.SetNull(a.ElementType),
			}
			replace := false
			for _, m := range a.PlanModifiers {
				resp := &planmodifier.SetResponse{PlanValue: req.PlanValue}
				m.PlanModifySet(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
				replace = replace || resp.RequiresReplace
			}
			return replace
		}
		return run(changed), run(types.SetUnknown(a.ElementType))

	case schema.MapAttribute:
		state := types.MapValueMust(a.ElementType, map[string]attr.Value{"k": elementValue(t, attrPath, a.ElementType, "a")})
		changed := types.MapValueMust(a.ElementType, map[string]attr.Value{"k": elementValue(t, attrPath, a.ElementType, "b")})
		run := func(planValue types.Map) bool {
			req := planmodifier.MapRequest{
				Path: path.Root(attrPath), State: updateState, Plan: updatePlan,
				StateValue: state, PlanValue: planValue, ConfigValue: types.MapNull(a.ElementType),
			}
			replace := false
			for _, m := range a.PlanModifiers {
				resp := &planmodifier.MapResponse{PlanValue: req.PlanValue}
				m.PlanModifyMap(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
				replace = replace || resp.RequiresReplace
			}
			return replace
		}
		return run(changed), run(types.MapUnknown(a.ElementType))

	case schema.SingleNestedAttribute:
		// Its children are walked separately. Object-level modifiers on the
		// nested attribute itself are unused here and would be ignored.
		if len(a.PlanModifiers) > 0 {
			t.Fatalf("%s: object-level plan modifiers are not replayed — extend replayModifiers", attrPath)
		}
		return false, false

	default:
		t.Fatalf("%s: unsupported attribute type %T — extend replayModifiers", attrPath, a)
		return false, false
	}
}

func elementValue(t *testing.T, attrPath string, elemType attr.Type, seed string) attr.Value {
	t.Helper()
	if elemType != types.StringType {
		t.Fatalf("%s: unsupported collection element type %s — extend elementValue", attrPath, elemType)
	}
	return types.StringValue("planmodifier-" + seed)
}

// Both RequiresReplace and UseStateForUnknown no-op when the raw state (create)
// or the raw plan (destroy) is null, so the replay only needs them non-null to
// count as an update. Neither modifier reads their contents.
var (
	nonNullRaw  = tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}, map[string]tftypes.Value{})
	updateState = tfsdk.State{Raw: nonNullRaw}
	updatePlan  = tfsdk.Plan{Raw: nonNullRaw}
)
