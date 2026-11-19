// Package nodemeta holds a node pool's Kubernetes node LABELS and TAINTS: the schema,
// the plan-time key rule, the wire conversion and the 409 refusals the kubernetes API
// gives for them. frostmoln_kubernetes_node_pool and the cluster's initial_node_pool
// both use it.
//
// The set is FIXED when the pool is created (the API refuses a changed set with 409
// node_meta_fixed), so both attributes RequiresReplace. A named pool cannot use
// create_before_destroy, so a replace destroys first: the key rule is checked at plan
// time so an invalid new set fails BEFORE the old pool is destroyed, not after.
package nodemeta

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The key rule is a COPY of servicekit provcmd ValidateKubernetesNodeLabels/Taints (the
// provider does not import servicekit). The server is the authority; this copy only
// moves its refusal to plan time. Keep the two in step.
const (
	MaxLabels = 32
	MaxTaints = 16
)

var (
	qualifiedNamePart = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	dnsSubdomain      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

	reservedDomains   = []string{"kubernetes.io", "k8s.io", "frostmoln.se", "frostmoln.cloud", "cilium.io"}
	reservedTaintKeys = map[string]bool{"ToBeDeletedByClusterAutoscaler": true, "DeletionCandidateOfClusterAutoscaler": true}
)

// Effects are the taint effects the API accepts.
var Effects = []string{"NoSchedule", "PreferNoSchedule", "NoExecute"}

// IsHard reports whether an effect keeps pods without a matching toleration off the node.
func IsHard(effect string) bool { return effect == "NoSchedule" || effect == "NoExecute" }

// ValidKey checks a label or taint key: a Kubernetes qualified name whose prefix is not
// a reserved domain.
func ValidKey(key string) error {
	if len(key) > 253+1+63 {
		return fmt.Errorf("key %q is longer than %d characters", key, 253+1+63)
	}
	prefix, name, hasPrefix := strings.Cut(key, "/")
	if !hasPrefix {
		name, prefix = prefix, ""
	}
	if len(name) > 63 || !qualifiedNamePart.MatchString(name) {
		return fmt.Errorf("key %q: the name must be 1-63 characters of [A-Za-z0-9-_.], starting and ending alphanumeric", key)
	}
	if hasPrefix {
		if len(prefix) > 253 || !dnsSubdomain.MatchString(prefix) {
			return fmt.Errorf("key %q: the prefix must be a lowercase DNS subdomain of at most 253 characters", key)
		}
		for _, d := range reservedDomains {
			if prefix == d || strings.HasSuffix(prefix, "."+d) {
				return fmt.Errorf("key %q: a prefix ending in %s is reserved for the platform", key, d)
			}
		}
	}
	return nil
}

// ValidValue checks a label or taint value: empty, or 1-63 characters of label-value syntax.
func ValidValue(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 63 || !qualifiedNamePart.MatchString(v) {
		return fmt.Errorf("value %q must be at most 63 characters of [A-Za-z0-9-_.], starting and ending alphanumeric", v)
	}
	return nil
}

// Taint is one element of the `taints` set.
type Taint struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

// TaintAttrTypes is the object type of one `taints` element.
var TaintAttrTypes = map[string]attr.Type{
	"key":    types.StringType,
	"value":  types.StringType,
	"effect": types.StringType,
}

// APITaint is the wire shape of a taint (kubernetes OpenAPI Taint).
type APITaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// fixedNote is the replacement warning; replaces is "pool" or "cluster".
func fixedNote(replaces string) string {
	if replaces == "cluster" {
		return " Fixed when the pool is created: changing it REPLACES the whole cluster, which is destroyed " +
			"(with its workloads) before the replacement is created. Unset and empty are the same set."
	}
	return " Fixed when the pool is created: changing it REPLACES the pool. A named pool is destroyed " +
		"before its replacement is created, so its nodes go away first. Unset and empty are the same set. " +
		"A key or value only known at apply is checked against the key rule only then."
}

// sameEmpty is the replace predicate: unset and empty are the same set on the wire
// (ToAPI omits both), so moving between them must not replace a pool or a cluster.
func sameEmpty(state, plan attr.Value) bool {
	n := func(v attr.Value) int {
		switch x := v.(type) {
		case types.Map:
			return len(x.Elements())
		case types.Set:
			return len(x.Elements())
		}
		return -1
	}
	return !plan.IsUnknown() && n(state) == 0 && n(plan) == 0
}

// LabelsAttribute is the `labels` schema attribute. replaces names what a change replaces.
func LabelsAttribute(replaces string) schema.MapAttribute {
	return schema.MapAttribute{
		Description: "Kubernetes node labels the platform applies to every node the pool boots (create, " +
			"scale, worker replacement, upgrade). NOT resource tags. Keys follow Kubernetes label syntax; " +
			"a prefix that is or ends in `kubernetes.io`, `k8s.io`, `frostmoln.se`, `frostmoln.cloud` or " +
			"`cilium.io` is reserved. At most 32." + fixedNote(replaces),
		Optional:    true,
		ElementType: types.StringType,
		Validators:  []validator.Map{labelsValidator{}},
		PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplaceIf(
			func(_ context.Context, req planmodifier.MapRequest, resp *mapplanmodifier.RequiresReplaceIfFuncResponse) {
				resp.RequiresReplace = !sameEmpty(req.StateValue, req.PlanValue)
			}, "Changing node labels replaces the resource.", "Changing node labels replaces the resource.")},
	}
}

// TaintsAttribute is the `taints` schema attribute. softOnly refuses NoSchedule and
// NoExecute (the cluster's initial pool, where the platform's own components run).
func TaintsAttribute(replaces string, softOnly bool) schema.SetNestedAttribute {
	desc := "Kubernetes node taints the platform applies to every node the pool boots. The key follows " +
		"label-key syntax with the same reserved prefixes; `ToBeDeletedByClusterAutoscaler` and " +
		"`DeletionCandidateOfClusterAutoscaler` are reserved too. At most 16. A cluster must keep one " +
		"running pool with nodes and no `NoSchedule`/`NoExecute` taint: the cluster's DNS, storage and " +
		"metrics components run there."
	effects := Effects
	if softOnly {
		desc += " The initial pool takes `PreferNoSchedule` only; put a `NoSchedule`/`NoExecute` taint on a " +
			"frostmoln_kubernetes_node_pool."
		effects = []string{"PreferNoSchedule"}
	}
	return schema.SetNestedAttribute{
		Description: desc + fixedNote(replaces),
		Optional:    true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"key": schema.StringAttribute{
					Description: "The taint key.",
					Required:    true,
					Validators:  []validator.String{keyValidator{taint: true}},
				},
				"value": schema.StringAttribute{
					Description: "The taint value (optional; label-value syntax).",
					Optional:    true,
					// "" would read back as null and plan a replacement forever.
					Validators: []validator.String{stringvalidator.LengthAtLeast(1), valueValidator{}},
				},
				"effect": schema.StringAttribute{
					Description: "One of " + strings.Join(effects, ", ") + ".",
					Required:    true,
					Validators:  []validator.String{effectValidator{allowed: effects}},
				},
			},
		},
		Validators: []validator.Set{taintsValidator{}},
		PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplaceIf(
			func(_ context.Context, req planmodifier.SetRequest, resp *setplanmodifier.RequiresReplaceIfFuncResponse) {
				resp.RequiresReplace = !sameEmpty(req.StateValue, req.PlanValue)
			}, "Changing node taints replaces the resource.", "Changing node taints replaces the resource.")},
	}
}

// ToAPI converts the configured sets into the create request's fields; null/unknown or
// empty yields nil (omitted).
func ToAPI(ctx context.Context, labels types.Map, taints types.Set, diags *diag.Diagnostics) (map[string]string, []APITaint) {
	var ls map[string]string
	if !labels.IsNull() && !labels.IsUnknown() && len(labels.Elements()) > 0 {
		diags.Append(labels.ElementsAs(ctx, &ls, false)...)
	}
	var ts []APITaint
	if !taints.IsNull() && !taints.IsUnknown() {
		var elems []Taint
		diags.Append(taints.ElementsAs(ctx, &elems, false)...)
		for _, t := range elems {
			ts = append(ts, APITaint{Key: t.Key.ValueString(), Value: t.Value.ValueString(), Effect: t.Effect.ValueString()})
		}
	}
	return ls, ts
}

// FromAPI renders the API's sets. An empty set reads back as the prior value when that
// was null or empty, so an unset attribute stays null (no diff, no replace).
func FromAPI(labels map[string]string, taints []APITaint, priorLabels types.Map, priorTaints types.Set, diags *diag.Diagnostics) (types.Map, types.Set) {
	var outLabels types.Map
	if len(labels) == 0 && !priorLabels.IsUnknown() && len(priorLabels.Elements()) == 0 {
		outLabels = priorLabels // null stays null, {} stays {}
	} else {
		elems := make(map[string]attr.Value, len(labels))
		for k, v := range labels {
			elems[k] = types.StringValue(v)
		}
		var d diag.Diagnostics
		outLabels, d = types.MapValue(types.StringType, elems)
		diags.Append(d...)
	}

	objType := types.ObjectType{AttrTypes: TaintAttrTypes}
	var outTaints types.Set
	if len(taints) == 0 && !priorTaints.IsUnknown() && len(priorTaints.Elements()) == 0 {
		outTaints = priorTaints
	} else {
		elems := make([]attr.Value, 0, len(taints))
		for _, t := range taints {
			value := types.StringNull()
			if t.Value != "" {
				value = types.StringValue(t.Value)
			}
			o, d := types.ObjectValue(TaintAttrTypes, map[string]attr.Value{
				"key": types.StringValue(t.Key), "value": value, "effect": types.StringValue(t.Effect),
			})
			diags.Append(d...)
			elems = append(elems, o)
		}
		var d diag.Diagnostics
		outTaints, d = types.SetValue(objType, elems)
		diags.Append(d...)
	}
	// A zero-value prior (no type) reads as null of the right type.
	NullUnset(&outLabels, &outTaints)
	return outLabels, outTaints
}

// Details reasons of the kubernetes API's node-meta 409s (code `conflict`).
const (
	ReasonFixed         = "node_meta_fixed"
	ReasonRetryMismatch = "node_meta_retry_mismatch"
	ReasonOnlyTainted   = "only_tainted_nodes"
)

// Refusal maps a node-meta 409 to an actionable diagnostic, branching on details.reason,
// never on the message. ok is false for any other error.
func Refusal(err error) (summary, detail string, ok bool) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Code != "conflict" {
		return "", "", false
	}
	reason, _ := apiErr.Details["reason"].(string)
	switch reason {
	case ReasonOnlyTainted:
		return "Change Would Leave Only Tainted Nodes",
			"The platform refused this change: afterwards every worker node would be in a pool with a " +
				"NoSchedule or NoExecute taint, and the cluster's DNS, storage and metrics components need a " +
				"node without one.\n\nAdd a node pool without a NoSchedule/NoExecute taint first (apply it on " +
				"its own and wait until it is active), then retry. A named pool is destroyed before its replacement is created, so " +
				"replacing the last such pool is refused too.\n\nUnderlying error: " + err.Error(), true
	case ReasonRetryMismatch:
		return "Failed Node Pool Has Different Labels Or Taints",
			"A failed node pool of this name exists, and re-creating it keeps the nodes the failed attempt " +
				"may have started, which carry that attempt's labels and taints. Delete the failed pool first, " +
				"or use the labels and taints it shows.\n\nUnderlying error: " + err.Error(), true
	case ReasonFixed:
		return "Node Pool Labels And Taints Are Fixed",
			"A node pool's labels and taints are fixed when it is created. Create a new pool with the new " +
				"set, move the workloads, and delete the old one.\n\nUnderlying error: " + err.Error(), true
	}
	return "", "", false
}

// AddError appends the tailored refusal for err if it is one, else a plain error.
func AddError(diags *diag.Diagnostics, summary string, err error) {
	if s, d, ok := Refusal(err); ok {
		diags.AddError(s, d)
		return
	}
	diags.AddError(summary, err.Error())
}

type keyValidator struct{ taint bool }

func (keyValidator) Description(context.Context) string {
	return "a Kubernetes label key whose prefix is not reserved"
}
func (v keyValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v keyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	k := req.ConfigValue.ValueString()
	if err := ValidKey(k); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Key", err.Error())
	} else if v.taint && reservedTaintKeys[k] {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Key", fmt.Sprintf("taint key %q is reserved for the platform", k))
	}
}

type valueValidator struct{}

func (valueValidator) Description(context.Context) string { return "a Kubernetes label value" }

func (v valueValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (valueValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := ValidValue(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Value", err.Error())
	}
}

type effectValidator struct{ allowed []string }

func (v effectValidator) Description(context.Context) string {
	return "one of " + strings.Join(v.allowed, ", ")
}

func (v effectValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v effectValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	e := req.ConfigValue.ValueString()
	if slices.Contains(v.allowed, e) {
		return
	}
	if slices.Contains(Effects, e) {
		// Only the initial pool narrows the set; this is the server's own reason.
		resp.Diagnostics.AddAttributeError(req.Path, "Taint Not Accepted On The Initial Pool",
			fmt.Sprintf("Taints with effect %s are not accepted on the initial pool: the cluster's DNS, storage and "+
				"metrics components run there. Add a frostmoln_kubernetes_node_pool with the taint instead.", e))
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid Taint Effect",
		fmt.Sprintf("effect %q must be one of %s", e, strings.Join(v.allowed, ", ")))
}

type labelsValidator struct{}

func (labelsValidator) Description(context.Context) string {
	return "at most 32 Kubernetes node labels with unreserved keys"
}

func (v labelsValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (labelsValidator) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > MaxLabels {
		resp.Diagnostics.AddAttributeError(req.Path, "Too Many Labels", fmt.Sprintf("at most %d labels per pool, got %d", MaxLabels, len(elems)))
	}
	for _, k := range slices.Sorted(maps.Keys(elems)) {
		if err := ValidKey(k); err != nil {
			resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(k), "Invalid Label Key", err.Error())
		}
		s, _ := elems[k].(types.String)
		if s.IsNull() {
			// Would fail at apply, after a destroy-first replace removed the old pool.
			resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(k), "Invalid Label Value",
				"a label value cannot be null; use \"\" for an empty value")
			continue
		}
		if !s.IsUnknown() {
			if err := ValidValue(s.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(k), "Invalid Label Value", err.Error())
			}
		}
	}
}

type taintsValidator struct{}

func (taintsValidator) Description(context.Context) string {
	return "at most 16 taints, each key+effect once"
}

func (v taintsValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (taintsValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := len(req.ConfigValue.Elements()); n > MaxTaints {
		resp.Diagnostics.AddAttributeError(req.Path, "Too Many Taints", fmt.Sprintf("at most %d taints per pool, got %d", MaxTaints, n))
	}
	var elems []Taint
	if d := req.ConfigValue.ElementsAs(ctx, &elems, false); d.HasError() {
		return // unknown members: checked again at apply-time plan
	}
	seen := map[string]bool{}
	for _, t := range elems {
		if t.Key.IsUnknown() || t.Effect.IsUnknown() {
			continue
		}
		id := t.Key.ValueString() + ":" + t.Effect.ValueString()
		if seen[id] {
			resp.Diagnostics.AddAttributeError(req.Path, "Duplicate Taint",
				fmt.Sprintf("taint %q with effect %s is given twice (with different values)", t.Key.ValueString(), t.Effect.ValueString()))
		}
		seen[id] = true
	}
}

// NullUnset gives a zero-value pair (a model built before these attributes existed,
// e.g. a test fixture) the typed nulls an unset configuration holds.
func NullUnset(labels *types.Map, taints *types.Set) {
	if labels.ElementType(context.Background()) == nil {
		*labels = types.MapNull(types.StringType)
	}
	if taints.ElementType(context.Background()) == nil {
		*taints = types.SetNull(types.ObjectType{AttrTypes: TaintAttrTypes})
	}
}
