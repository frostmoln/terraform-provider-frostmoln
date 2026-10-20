package idform

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// sameAttr compares two framework values at path up to id spelling, strictly:
// a public id of another kind is a different id.
func sameAttr(ctx context.Context, a, b attr.Value, isID func(string) bool, path string) bool {
	ta, err := a.ToTerraformValue(ctx)
	if err != nil {
		return false
	}
	tb, err := b.ToTerraformValue(ctx)
	if err != nil {
		return false
	}
	return same(ta, tb, rules{strict, isID}, path)
}

// The modifier wrappers run the resource's own modifier, then drop a
// replacement it asked for when the planned and prior values name the same ids.
// They are the plan's RequiresReplaceIf(!decodeEqual(config, state)), applied
// to every modifier so no schema has to opt in; only values at id paths
// compare as ids. Exact equality never
// reaches here: RequiresReplace and RequiresReplaceIf both return early on it.

type at struct {
	isID func(string) bool
	path string
}

type stringMod struct {
	planmodifier.String
	at
}

func (m stringMod) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	m.String.PlanModifyString(ctx, req, resp)
	if resp.RequiresReplace && sameAttr(ctx, resp.PlanValue, req.StateValue, m.isID, m.path) {
		resp.RequiresReplace = false
	}
}

type listMod struct {
	planmodifier.List
	at
}

func (m listMod) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	m.List.PlanModifyList(ctx, req, resp)
	if resp.RequiresReplace && sameAttr(ctx, resp.PlanValue, req.StateValue, m.isID, m.path) {
		resp.RequiresReplace = false
	}
}

type setMod struct {
	planmodifier.Set
	at
}

func (m setMod) PlanModifySet(ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	m.Set.PlanModifySet(ctx, req, resp)
	if resp.RequiresReplace && sameAttr(ctx, resp.PlanValue, req.StateValue, m.isID, m.path) {
		resp.RequiresReplace = false
	}
}

type objectMod struct {
	planmodifier.Object
	at
}

func (m objectMod) PlanModifyObject(ctx context.Context, req planmodifier.ObjectRequest, resp *planmodifier.ObjectResponse) {
	m.Object.PlanModifyObject(ctx, req, resp)
	if resp.RequiresReplace && sameAttr(ctx, resp.PlanValue, req.StateValue, m.isID, m.path) {
		resp.RequiresReplace = false
	}
}

// schemaWrap wraps every string, list, set and object plan modifier. Which
// values compare as ids is decided at the leaves (rules.isID), so a modifier
// on a non-id attribute keeps every replacement it asks for. It records each
// computed-only attribute's dotted path in computedOnly.
type schemaWrap struct {
	isID         func(string) bool
	computedOnly map[string]bool
}

func (w schemaWrap) strings(ms []planmodifier.String, p string) []planmodifier.String {
	out := make([]planmodifier.String, len(ms))
	for i, m := range ms {
		out[i] = stringMod{m, at{w.isID, p}}
	}
	return out
}

func (w schemaWrap) lists(ms []planmodifier.List, p string) []planmodifier.List {
	out := make([]planmodifier.List, len(ms))
	for i, m := range ms {
		out[i] = listMod{m, at{w.isID, p}}
	}
	return out
}

func (w schemaWrap) sets(ms []planmodifier.Set, p string) []planmodifier.Set {
	out := make([]planmodifier.Set, len(ms))
	for i, m := range ms {
		out[i] = setMod{m, at{w.isID, p}}
	}
	return out
}

func (w schemaWrap) objects(ms []planmodifier.Object, p string) []planmodifier.Object {
	out := make([]planmodifier.Object, len(ms))
	for i, m := range ms {
		out[i] = objectMod{m, at{w.isID, p}}
	}
	return out
}

func (w schemaWrap) schema(s *schema.Schema) {
	s.Attributes = w.attrs(s.Attributes, "")
	s.Blocks = w.blocks(s.Blocks, "")
}

func (w schemaWrap) attrs(attrs map[string]schema.Attribute, path string) map[string]schema.Attribute {
	if attrs == nil {
		return nil
	}
	out := make(map[string]schema.Attribute, len(attrs))
	for name, a := range attrs {
		p := join(path, name)
		if a.IsComputed() && !a.IsOptional() && !a.IsRequired() {
			w.computedOnly[p] = true
		}
		switch t := a.(type) {
		case schema.StringAttribute:
			t.PlanModifiers = w.strings(t.PlanModifiers, p)
			a = t
		case schema.ListAttribute:
			t.PlanModifiers = w.lists(t.PlanModifiers, p)
			a = t
		case schema.SetAttribute:
			t.PlanModifiers = w.sets(t.PlanModifiers, p)
			a = t
		case schema.ObjectAttribute:
			t.PlanModifiers = w.objects(t.PlanModifiers, p)
			a = t
		case schema.ListNestedAttribute:
			t.PlanModifiers = w.lists(t.PlanModifiers, p)
			t.NestedObject.PlanModifiers = w.objects(t.NestedObject.PlanModifiers, p)
			t.NestedObject.Attributes = w.attrs(t.NestedObject.Attributes, p)
			a = t
		case schema.SetNestedAttribute:
			t.PlanModifiers = w.sets(t.PlanModifiers, p)
			t.NestedObject.PlanModifiers = w.objects(t.NestedObject.PlanModifiers, p)
			t.NestedObject.Attributes = w.attrs(t.NestedObject.Attributes, p)
			a = t
		case schema.MapNestedAttribute:
			t.NestedObject.PlanModifiers = w.objects(t.NestedObject.PlanModifiers, p)
			t.NestedObject.Attributes = w.attrs(t.NestedObject.Attributes, p)
			a = t
		case schema.SingleNestedAttribute:
			t.PlanModifiers = w.objects(t.PlanModifiers, p)
			t.Attributes = w.attrs(t.Attributes, p)
			a = t
		}
		out[name] = a
	}
	return out
}

func (w schemaWrap) blocks(blocks map[string]schema.Block, path string) map[string]schema.Block {
	if blocks == nil {
		return nil
	}
	out := make(map[string]schema.Block, len(blocks))
	for name, b := range blocks {
		p := join(path, name)
		switch t := b.(type) {
		case schema.ListNestedBlock:
			t.PlanModifiers = w.lists(t.PlanModifiers, p)
			t.NestedObject.PlanModifiers = w.objects(t.NestedObject.PlanModifiers, p)
			t.NestedObject.Attributes = w.attrs(t.NestedObject.Attributes, p)
			t.NestedObject.Blocks = w.blocks(t.NestedObject.Blocks, p)
			b = t
		case schema.SetNestedBlock:
			t.PlanModifiers = w.sets(t.PlanModifiers, p)
			t.NestedObject.PlanModifiers = w.objects(t.NestedObject.PlanModifiers, p)
			t.NestedObject.Attributes = w.attrs(t.NestedObject.Attributes, p)
			t.NestedObject.Blocks = w.blocks(t.NestedObject.Blocks, p)
			b = t
		case schema.SingleNestedBlock:
			t.PlanModifiers = w.objects(t.PlanModifiers, p)
			t.Attributes = w.attrs(t.Attributes, p)
			t.Blocks = w.blocks(t.Blocks, p)
			b = t
		}
		out[name] = b
	}
	return out
}
