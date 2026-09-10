// Package scopedecl holds the machine-readable authoritative-scope declaration
// for every resource in the provider: which fields the platform ENACTS and the
// provider RECONCILES on refresh, which fields it only OBSERVES (the platform
// may change or assign them under the customer), which are CREATE-IMMUTABLE
// with the reason recorded next to the marker, and the policy for each
// platform-invented default.
//
// This is Mechanic 1 of the Terraform Surface Contract program. The schema
// cannot say these things itself: a RequiresReplace plan modifier carries no
// reason, Computed carries no who-may-change-it, and platform-invented
// defaults live outside the schema entirely. The declaration here is the
// customer-visible contract: Summary renders it into every resource's
// generated page, and internal/provider/scope_declarations_test.go asserts it
// against the live schema — every resource has an entry, every attribute
// whose change forces a replacement is listed under Immutable with a reason,
// every Observes path is a Computed-only attribute, and every resource's
// description carries its Summary.
//
// The map holds only what the schema cannot derive. The test derives the full
// per-field partition itself (Computed-only -> observed, replaces-on-change ->
// create-immutable, everything else configurable -> enacted and reconciled)
// and fails when the map and the schema disagree, so the two cannot drift
// apart silently.
package scopedecl

import (
	"sort"
	"strings"
)

// DefaultPolicy is the policy for one platform-invented default — an object
// that exists before the customer creates anything. The three policies are
// the surface-contract doctrine's: see templates/guides/surface-contract.md.tmpl.
type DefaultPolicy string

const (
	// DeleteOnCreate — the default is not load-bearing; the resource removes
	// it as part of Create (AWS's default-egress behaviour).
	DeleteOnCreate DefaultPolicy = "delete-on-create"

	// AdoptAsManaged — the default is load-bearing; a dedicated resource
	// adopts it into management (the aws_default_* family pattern).
	AdoptAsManaged DefaultPolicy = "adopt-as-managed"

	// KeepWithDocs — the default stays; the surface documents that Terraform
	// can see it (or deliberately not) but does not manage it.
	KeepWithDocs DefaultPolicy = "keep-with-docs"
)

// Field is one attribute path with the reason for its classification. Paths
// are dotted for attributes inside a nested attribute
// ("initial_node_pool.name"), the same convention as
// internal/provider/planmodifier_order_test.go's mustReplaceOnRealChange.
type Field struct {
	Path string
	Why  string
}

// PlatformDefault is one platform-invented default touching the resource,
// with the policy chosen for it and the reason.
type PlatformDefault struct {
	Name   string
	Policy DefaultPolicy
	Why    string
}

// Decl is the authoritative-scope declaration for one resource type.
//
// A resource whose fields need no annotation still gets an entry — the walk
// test checks every registered resource against the map in both directions,
// so a new resource cannot ship without its scope being declared.
type Decl struct {
	// ImmutableWhy is the reason a change to any Immutable path forces a
	// replacement, used where the entry carries no Why of its own. One of the
	// two must resolve non-empty for every Immutable entry — the work item's
	// "reason recorded next to the marker" is not optional.
	ImmutableWhy string

	// Immutable lists the CREATE-IMMUTABLE attributes: a change forces
	// replacement. The walk test asserts this is exactly the set of
	// attributes whose plan modifiers require replacement on a real value
	// change, in both directions.
	Immutable []Field

	// ImmutableWithoutReplace lists create-only attributes whose change is
	// REFUSED rather than replaced — the frostmoln_secret pattern, where
	// RequiresReplace would destroy a live object and then fail to re-create
	// it under the same name. They render as create-immutable with the
	// refusal spelled out; the walk test asserts they carry NO replacement
	// modifier (a plain RequiresReplace would satisfy the test while
	// destroying something it cannot rebuild).
	ImmutableWithoutReplace []Field

	// Observes labels the Computed attributes the platform may change or
	// assign under the customer — the honesty-critical ones, not every
	// computed field: platform-managed constructs (the managed-webserver
	// security group), platform-assigned addresses, live status. The walk
	// test asserts each is Computed-only in the schema.
	Observes []Field

	// EnactsExcept notes configurable attributes the generic "enacted and
	// reconciled" bullet does NOT cover, with the reason: write-only values
	// (sent, never stored or read back) and acknowledgement/behaviour flags
	// carried in state rather than applied as platform settings. Without
	// these the rendered bullet would claim a refresh reads the truth back
	// for attributes where that is false.
	EnactsExcept []Field

	// Defaults declares the platform-invented defaults touching the
	// resource, one entry per default, with the doctrine policy.
	Defaults []PlatformDefault

	// EnactsNone is set when no configurable attribute updates in place —
	// either every configurable attribute is create-immutable
	// (frostmoln_vpc_route) or the resource has none at all
	// (frostmoln_container_registry). The walk test asserts this agrees with
	// the schema.
	EnactsNone bool
}

// Summary renders the resource's declaration as the markdown appended to its
// schema Description — the customer-visible form of the contract, regenerated
// into docs/resources/*.md by make generate.
//
// The leading sentence names no class when the declaration is empty: a
// resource with nothing platform-mutated, nothing immutable and no invented
// defaults gets one honest sentence, not an empty framework.
func Summary(typeName string) string {
	decl, declared := Declarations[typeName]
	if !declared {
		return "**Authoritative scope.** UNDECLARED — the provider test suite fails " +
			"until this resource is added to scopedecl.Declarations."
	}

	if len(decl.Immutable) == 0 && len(decl.ImmutableWithoutReplace) == 0 &&
		len(decl.Observes) == 0 && len(decl.Defaults) == 0 && len(decl.EnactsExcept) == 0 {
		if decl.EnactsNone {
			return "**Authoritative scope.** This resource has no configurable attributes: " +
				"everything on it is platform-assigned and read back on refresh. Declared in " +
				"`internal/scopedecl`, machine-checked against the schema."
		}
		return "**Authoritative scope.** Every configurable attribute of this resource is " +
			"**enacted and reconciled**: the platform applies it and a refresh reads the truth " +
			"back. No attribute forces replacement, and no platform-invented default attaches. " +
			"Declared in `internal/scopedecl`, machine-checked against the schema."
	}

	var b strings.Builder
	b.WriteString("**Authoritative scope** — who owns what on this resource, declared in " +
		"`internal/scopedecl` and machine-checked against the schema.")

	if !decl.EnactsNone {
		b.WriteString("\n\n**Enacted and reconciled** — every configurable attribute not " +
			"listed below: the platform applies it, and a refresh reads the truth back.")
	}

	if len(decl.Immutable) > 0 {
		for _, group := range groupByReason(decl.Immutable, decl.ImmutableWhy) {
			b.WriteString("\n\n**Create-immutable** — ")
			b.WriteString(quotePaths(group.paths))
			b.WriteString(": ")
			b.WriteString(group.reason)
			if !strings.HasSuffix(group.reason, ".") {
				b.WriteString(".")
			}
		}
	}

	if len(decl.ImmutableWithoutReplace) > 0 {
		for _, group := range groupByReason(decl.ImmutableWithoutReplace, "") {
			b.WriteString("\n\n**Create-only, change refused (not replaced)** — ")
			b.WriteString(quotePaths(group.paths))
			b.WriteString(": ")
			b.WriteString(group.reason)
			if !strings.HasSuffix(group.reason, ".") {
				b.WriteString(".")
			}
		}
	}

	if len(decl.EnactsExcept) > 0 {
		b.WriteString("\n\n**Not enacted state** — ")
		entries := make([]string, 0, len(decl.EnactsExcept))
		for _, f := range sortedFields(decl.EnactsExcept) {
			why := f.Why
			if !strings.HasSuffix(why, ".") {
				why += "."
			}
			entries = append(entries, "`"+f.Path+"`: "+why)
		}
		b.WriteString(strings.Join(entries, " "))
	}

	if len(decl.Observes) > 0 {
		b.WriteString("\n\n**Observed, not enacted** — ")
		entries := make([]string, 0, len(decl.Observes))
		for _, f := range sortedFields(decl.Observes) {
			why := f.Why
			if !strings.HasSuffix(why, ".") {
				why += "."
			}
			entries = append(entries, "`"+f.Path+"`: "+why)
		}
		b.WriteString(strings.Join(entries, " "))
	}

	for _, def := range decl.Defaults {
		b.WriteString("\n\n**Platform-invented default** — ")
		b.WriteString(def.Name)
		b.WriteString(": **")
		b.WriteString(string(def.Policy))
		b.WriteString("**. ")
		b.WriteString(def.Why)
		if !strings.HasSuffix(def.Why, ".") {
			b.WriteString(".")
		}
	}

	return b.String()
}

// reasonGroup is one set of Immutable paths sharing one resolved reason.
type reasonGroup struct {
	reason string
	paths  []string
}

// groupByReason groups Immutable entries by their resolved reason, keeping
// first-seen reason order and sorting paths within a group, so the rendered
// line reads "`a`, `b`, `c`: reason" instead of one bullet per attribute.
func groupByReason(fields []Field, defaultWhy string) []reasonGroup {
	groups := []reasonGroup{}
	index := map[string]int{}
	for _, f := range fields {
		why := f.Why
		if why == "" {
			why = defaultWhy
		}
		i, found := index[why]
		if !found {
			i = len(groups)
			index[why] = i
			groups = append(groups, reasonGroup{reason: why, paths: []string{}})
		}
		groups[i].paths = append(groups[i].paths, f.Path)
	}
	for i := range groups {
		sort.Strings(groups[i].paths)
	}
	return groups
}

func quotePaths(paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, "`"+p+"`")
	}
	return strings.Join(quoted, ", ")
}

func sortedFields(fields []Field) []Field {
	out := make([]Field, len(fields))
	copy(out, fields)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
