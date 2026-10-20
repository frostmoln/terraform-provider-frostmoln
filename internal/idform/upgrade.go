package idform

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
)

// rewrite returns v with every canonical-UUID id the kind map names in public
// form. A map key is a dotted attribute path with element steps omitted, so
// "rules.id" covers every element of a list, set or map of objects, and a list
// or set of strings is rewritten element by element. The value is a template
// split like the id itself on "/" and ":": "ins/nic" for "instanceId/portId",
// "agw:-" for "gwID:revision". A template part that is not a prefix ("-"),
// a part that is not a canonical UUID, and a value whose shape differs from the
// template are left as they are, so the rewrite is idempotent and never guesses.
func rewrite(v tftypes.Value, kinds map[string]string) tftypes.Value {
	return rewriteAt(v, kinds, "")
}

func rewriteAt(v tftypes.Value, kinds map[string]string, path string) tftypes.Value {
	if !v.IsKnown() || v.IsNull() {
		return v
	}
	typ := v.Type()
	switch {
	case typ.Is(tftypes.String):
		tmpl, ok := kinds[path]
		if !ok {
			return v
		}
		var s string
		_ = v.As(&s)
		return tftypes.NewValue(typ, applyTemplate(s, tmpl))
	case typ.Is(tftypes.Object{}):
		var m map[string]tftypes.Value
		_ = v.As(&m)
		out := make(map[string]tftypes.Value, len(m))
		for k, e := range m {
			out[k] = rewriteAt(e, kinds, join(path, k))
		}
		return tftypes.NewValue(typ, out)
	case typ.Is(tftypes.Map{}):
		var m map[string]tftypes.Value
		_ = v.As(&m)
		out := make(map[string]tftypes.Value, len(m))
		for k, e := range m {
			out[k] = rewriteAt(e, kinds, path)
		}
		return tftypes.NewValue(typ, out)
	case typ.Is(tftypes.List{}), typ.Is(tftypes.Set{}), typ.Is(tftypes.Tuple{}):
		var l []tftypes.Value
		_ = v.As(&l)
		out := make([]tftypes.Value, len(l))
		for i, e := range l {
			out[i] = rewriteAt(e, kinds, path)
		}
		return tftypes.NewValue(typ, out)
	}
	return v
}

// applyTemplate rewrites s by the first "|"-separated alternative of tmpl
// whose shape it has. A trailing "*" part matches the rest of s verbatim (a
// CIDR, which carries both separators).
func applyTemplate(s, tmpl string) string {
	for _, alt := range strings.Split(tmpl, "|") {
		parts, want := tokens(s), tokens(alt)
		if n := len(want); want[n-1] == "*" && len(parts) >= n {
			parts = append(parts[:n-1:n-1], strings.Join(parts[n-1:], ""))
		}
		if len(parts) != len(want) {
			continue
		}
		shape := true
		for i := 1; i < len(parts); i += 2 {
			shape = shape && parts[i] == want[i]
		}
		if !shape {
			continue
		}
		for i := 0; i < len(parts); i += 2 {
			// Encode refuses anything but a canonical UUID, so a public id or
			// any other part is left as it is.
			if enc, ok := pubid.Encode(want[i], parts[i]); ok {
				parts[i] = enc
			}
		}
		return strings.Join(parts, "")
	}
	return s
}

// upgraders returns the resource's state upgraders for the bumped schema
// version: every prior version the resource could already upgrade from runs
// its own upgrader and then the id rewrite, and the version before the bump
// gets the id rewrite alone, read from the raw state against the current
// schema (the bump changes no attribute).
func upgraders(ctx context.Context, inner map[int64]resource.StateUpgrader, prior int64, kinds map[string]string) map[int64]resource.StateUpgrader {
	out := make(map[int64]resource.StateUpgrader, len(inner)+1)
	for v, u := range inner {
		out[v] = resource.StateUpgrader{
			PriorSchema: u.PriorSchema,
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				u.StateUpgrader(ctx, req, resp)
				if resp.Diagnostics.HasError() {
					return
				}
				if resp.DynamicValue != nil {
					val, err := resp.DynamicValue.Unmarshal(resp.State.Schema.Type().TerraformType(ctx))
					if err != nil {
						resp.Diagnostics.AddError("Failed to read upgraded state", err.Error())
						return
					}
					resp.DynamicValue = nil
					resp.State.Raw = val
				}
				resp.State.Raw = rewrite(resp.State.Raw, kinds)
			},
		}
	}
	out[prior] = resource.StateUpgrader{
		StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			typ := resp.State.Schema.Type().TerraformType(ctx)
			val, err := req.RawState.UnmarshalWithOpts(typ, tfprotov6.UnmarshalOpts{
				ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true},
			})
			if err != nil {
				resp.Diagnostics.AddError("Failed to read prior state for the public-id upgrade", err.Error())
				return
			}
			resp.State.Raw = rewrite(val, kinds)
		},
	}
	return out
}
