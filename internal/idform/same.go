// Package idform carries the provider through the switch from UUID to public
// ids (ADR-0129, PUBLIC-IDS-PLAN D6 "Terraform provider"). One wrapper around
// every resource does four things, so no resource has to:
//
//   - its schema version goes up by one, and a state upgrader rewrites the ids
//     the kind map in kinds.go names into public form;
//   - every attribute plan modifier is wrapped so a replacement is dropped when
//     config and state name the same id in two spellings (decoded bytes, and
//     the kind when both sides are public);
//   - Create/Update keep a planned id the API echoed in the other spelling, and
//     Read keeps a prior one, so a UUID hardcoded in HCL never diffs again;
//   - Update returns without calling the API when plan and state differ only in
//     id form.
package idform

import (
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
)

// strict compares kinds when both sides are public ids; loose compares only
// the UUID bytes.
type mode bool

const (
	strict mode = true
	loose  mode = false
)

// rules says how to compare: the mode, and which dotted attribute paths
// (element steps omitted) hold ids. A string anywhere else compares exactly, so
// a tag value, a DNS record or a policy document that merely contains a UUID is
// never treated as an id.
type rules struct {
	m    mode
	isID func(path string) bool
}

// idName reports whether an attribute's own name says it holds ids: id, *_id
// or *_ids.
func idName(path string) bool {
	name := path[strings.LastIndex(path, ".")+1:]
	return name == "id" || strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_ids")
}

// sameString reports whether a and b are the same string, or the same ids in
// two spellings. Composite ids ("ins_…/nic_…", "agw_…:3") compare part by part
// on "/" and ":"; every non-id part must match exactly.
func sameString(a, b string, m mode) bool {
	if a == b {
		return true
	}
	ta, tb := tokens(a), tokens(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] == tb[i] {
			continue
		}
		ua, pa, oka := pubid.Canonical(ta[i])
		ub, pb, okb := pubid.Canonical(tb[i])
		// A UUID part counts only in its canonical lowercase spelling, the one
		// the platform reports: an upper-case UUID is text, never an id.
		if !oka || !okb || ua != ub || (pa == "" && ta[i] != ua) || (pb == "" && tb[i] != ub) {
			return false
		}
		if m == strict && pa != "" && pb != "" && pubid.Kind(pa) != pubid.Kind(pb) {
			return false
		}
	}
	return true
}

// tokens splits s into parts and the separators between them, so two
// composites match only with the same separators in the same places.
func tokens(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == ':' {
			out = append(out, s[start:i], s[i:i+1])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// same reports whether a and b, found at path, are equal values up to the
// spelling of the ids at id paths. An unknown is the same only as another
// unknown.
func same(a, b tftypes.Value, r rules, path string) bool {
	if !a.IsKnown() {
		return !b.IsKnown()
	}
	if !b.IsKnown() || a.IsNull() != b.IsNull() {
		return false
	}
	if a.IsNull() {
		return true
	}
	if !a.Type().Equal(b.Type()) {
		return false
	}
	switch {
	case a.Type().Is(tftypes.String):
		var sa, sb string
		_ = a.As(&sa)
		_ = b.As(&sb)
		if !r.isID(path) {
			return sa == sb
		}
		return sameString(sa, sb, r.m)
	case a.Type().Is(tftypes.Object{}), a.Type().Is(tftypes.Map{}):
		var ma, mb map[string]tftypes.Value
		_ = a.As(&ma)
		_ = b.As(&mb)
		if len(ma) != len(mb) {
			return false
		}
		object := a.Type().Is(tftypes.Object{})
		for k, va := range ma {
			at := path
			if object {
				at = join(path, k)
			}
			vb, ok := mb[k]
			if !ok || !same(va, vb, r, at) {
				return false
			}
		}
		return true
	case a.Type().Is(tftypes.List{}), a.Type().Is(tftypes.Tuple{}):
		var la, lb []tftypes.Value
		_ = a.As(&la)
		_ = b.As(&lb)
		if len(la) != len(lb) {
			return false
		}
		for i := range la {
			if !same(la[i], lb[i], r, path) {
				return false
			}
		}
		return true
	case a.Type().Is(tftypes.Set{}):
		var la, lb []tftypes.Value
		_ = a.As(&la)
		_ = b.As(&lb)
		return len(la) == len(lb) && len(matchSet(la, lb, r, path)) == len(la)
	}
	return a.Equal(b)
}

// matchSet pairs each element of a with a distinct element of b it is the same
// as; the result maps a's index to b's.
func matchSet(a, b []tftypes.Value, r rules, path string) map[int]int {
	pairs := map[int]int{}
	used := make([]bool, len(b))
	for i, va := range a {
		for j, vb := range b {
			if !used[j] && same(va, vb, r, path) {
				pairs[i], used[j] = j, true
				break
			}
		}
	}
	return pairs
}

// keepPrior returns next with every id that is the same as prior's at the
// same position replaced by prior's spelling. Positions whose dotted attribute
// path (element steps omitted) is in skip always take next's value.
func keepPrior(next, prior tftypes.Value, r rules, skip map[string]bool) tftypes.Value {
	return keepAt(next, prior, r, skip, "")
}

func keepAt(next, prior tftypes.Value, r rules, skip map[string]bool, path string) tftypes.Value {
	if skip[path] || !next.IsKnown() || next.IsNull() || !prior.IsKnown() || prior.IsNull() || !next.Type().Equal(prior.Type()) {
		return next
	}
	typ := next.Type()
	switch {
	case typ.Is(tftypes.String):
		if next.Equal(prior) {
			return next
		}
		if same(prior, next, r, path) {
			return prior
		}
		return next
	case typ.Is(tftypes.Object{}):
		var mn, mp map[string]tftypes.Value
		_ = next.As(&mn)
		_ = prior.As(&mp)
		out := make(map[string]tftypes.Value, len(mn))
		for k, v := range mn {
			if p, ok := mp[k]; ok {
				v = keepAt(v, p, r, skip, join(path, k))
			}
			out[k] = v
		}
		return tftypes.NewValue(typ, out)
	case typ.Is(tftypes.Map{}):
		var mn, mp map[string]tftypes.Value
		_ = next.As(&mn)
		_ = prior.As(&mp)
		out := make(map[string]tftypes.Value, len(mn))
		for k, v := range mn {
			if p, ok := mp[k]; ok {
				v = keepAt(v, p, r, skip, path)
			}
			out[k] = v
		}
		return tftypes.NewValue(typ, out)
	case typ.Is(tftypes.List{}), typ.Is(tftypes.Tuple{}):
		var ln, lp []tftypes.Value
		_ = next.As(&ln)
		_ = prior.As(&lp)
		out := make([]tftypes.Value, len(ln))
		for i, v := range ln {
			if i < len(lp) {
				v = keepAt(v, lp[i], r, skip, path)
			}
			out[i] = v
		}
		return tftypes.NewValue(typ, out)
	case typ.Is(tftypes.Set{}):
		var ln, lp []tftypes.Value
		_ = next.As(&ln)
		_ = prior.As(&lp)
		pairs := matchSet(ln, lp, r, path)
		out := make([]tftypes.Value, len(ln))
		for i, v := range ln {
			if j, ok := pairs[i]; ok {
				v = keepAt(v, lp[j], r, skip, path)
			}
			out[i] = v
		}
		return tftypes.NewValue(typ, out)
	}
	return next
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
