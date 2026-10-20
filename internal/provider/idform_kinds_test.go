package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/idform"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
)

// TestIDFormKinds holds the state upgrader's kind map to the live schemas: every
// type is a registered resource, every path names a string (or a list or set of
// strings) at that position, and every template part is "-", "*" or a
// registered prefix that is not an input-only alias (the API never renders one).
func TestIDFormKinds(t *testing.T) {
	ctx := context.Background()
	p := New("test")().(*FrostmolnProvider)
	types := map[string]tftypes.Type{}
	for _, f := range p.Resources(ctx) {
		r := f()
		var m resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &m)
		var s resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &s)
		types[m.TypeName] = s.Schema.Type().TerraformType(ctx)
	}
	registered := map[string]bool{}
	for _, k := range pubid.Registry.Kinds {
		registered[k.Prefix] = k.RendersAs == ""
	}
	for typeName, paths := range idform.Kinds {
		typ, ok := types[typeName]
		if !ok {
			t.Errorf("%s is not a resource", typeName)
			continue
		}
		for path, tmpl := range paths {
			if !isStringAt(typ, strings.Split(path, ".")) {
				t.Errorf("%s.%s is not a string, or a list or set of strings", typeName, path)
			}
			for _, alt := range strings.Split(tmpl, "|") {
				for _, part := range strings.FieldsFunc(alt, func(r rune) bool { return r == '/' || r == ':' }) {
					if part != "-" && part != "*" && !registered[part] {
						t.Errorf("%s.%s: %q is not a rendered servicekit prefix", typeName, path, part)
					}
				}
			}
		}
	}
}

func isStringAt(typ tftypes.Type, path []string) bool {
	for {
		switch t := typ.(type) {
		case tftypes.List:
			typ = t.ElementType
			continue
		case tftypes.Set:
			typ = t.ElementType
			continue
		case tftypes.Map:
			typ = t.ElementType
			continue
		}
		break
	}
	if len(path) == 0 {
		return typ.Is(tftypes.String)
	}
	o, ok := typ.(tftypes.Object)
	if !ok {
		return false
	}
	next, ok := o.AttributeTypes[path[0]]
	return ok && isStringAt(next, path[1:])
}

// TestIDFormUpgradesEveryResource runs the real upgrader of every registered
// resource from its pre-switch version over a raw state whose top-level kind-map
// ids are UUIDs (composites built from their template), and checks each comes
// back in public form. It also refuses an inner upgrader keyed at the inner
// current version, which the wrapper's own upgrader would silently replace.
func TestIDFormUpgradesEveryResource(t *testing.T) {
	ctx := context.Background()
	const u = "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01"
	p := New("test")().(*FrostmolnProvider)
	for _, f := range p.Resources(ctx) {
		r := f()
		var m resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &m)
		var s resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &s)
		prior := s.Schema.Version - 1
		if inner, ok := r.(interface{ Inner() resource.Resource }).Inner().(resource.ResourceWithUpgradeState); ok {
			if _, clash := inner.UpgradeState(ctx)[prior]; clash {
				t.Errorf("%s: an inner upgrader is keyed at its current version %d", m.TypeName, prior)
			}
		}
		u2, ok := r.(resource.ResourceWithUpgradeState).UpgradeState(ctx)[prior]
		if !ok {
			t.Errorf("%s: no upgrader from v%d", m.TypeName, prior)
			continue
		}
		raw := map[string]any{}
		want := map[string]string{}
		attrs := s.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes
		for path, tmpl := range idform.Kinds[m.TypeName] {
			typ, top := attrs[path]
			if !top {
				continue // nested paths are covered by the idform unit tests
			}
			alt := strings.Split(tmpl, "|")[0]
			in, out := fixture(t, alt, u)
			if typ.Is(tftypes.String) {
				raw[path], want[path] = in, out
			} else {
				raw[path], want[path] = []string{in}, out
			}
		}
		js, _ := json.Marshal(raw)
		resp := resource.UpgradeStateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}}
		u2.StateUpgrader(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: js}}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: %v", m.TypeName, resp.Diagnostics)
			continue
		}
		var got map[string]tftypes.Value
		_ = resp.State.Raw.As(&got)
		for path, w := range want {
			v := got[path]
			var str string
			if v.Type().Is(tftypes.String) {
				_ = v.As(&str)
			} else {
				var l []tftypes.Value
				_ = v.As(&l)
				if len(l) == 1 {
					_ = l[0].As(&str)
				}
			}
			if str != w {
				t.Errorf("%s.%s upgraded to %q, want %q", m.TypeName, path, str, w)
			}
		}
	}
}

// fixture builds a UUID-form value of a template and its expected public form.
func fixture(t *testing.T, tmpl, u string) (in, out string) {
	t.Helper()
	sep := func(r rune) bool { return r == '/' || r == ':' }
	// Walk the template keeping its separators.
	var cur string
	flush := func() {
		switch cur {
		case "-":
			in, out = in+"x", out+"x"
		case "*":
			in, out = in+"rest/of:it", out+"rest/of:it"
		default:
			enc, ok := pubid.Encode(cur, u)
			if !ok {
				t.Fatalf("template part %q", cur)
			}
			in, out = in+u, out+enc
		}
		cur = ""
	}
	for _, r := range tmpl {
		if sep(r) {
			flush()
			in, out = in+string(r), out+string(r)
			continue
		}
		cur += string(r)
	}
	flush()
	return in, out
}
