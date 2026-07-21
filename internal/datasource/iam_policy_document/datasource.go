// Package iam_policy_document implements the frostmoln_iam_policy_document data
// source: a pure, offline composer that turns HCL `rule` / `constraint` blocks
// into a Frostmoln access-policy JSON document (ADR-0102), exposed as `json` for
// a frostmoln_iam_policy resource. It mirrors the ergonomics of AWS's
// aws_iam_policy_document but uses Frostmoln-native vocabulary (rule / access /
// operations / targets / constraints / FRN — not statement / effect / action /
// resource / condition). It makes NO API call; the server validates operations
// against the append-only catalog when the policy is created.
package iam_policy_document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// schemaVersion is the sole access-policy schema version (servicekit authz
// SchemaVersion, stable at "1" — ADR-0102 append-only contract).
const schemaVersion = "1"

// constraintOperators is the P1 operator vocabulary (servicekit
// authz.p1ConstraintOperators). ipInRange takes a list of CIDRs; every other
// operator takes a single scalar value (enforced in buildPolicyJSON).
var constraintOperators = []string{"equals", "notEquals", "like", "ipInRange", "before", "after"}

var _ datasource.DataSource = &documentDataSource{}

// NewDataSource returns a new frostmoln_iam_policy_document data source factory.
func NewDataSource() datasource.DataSource {
	return &documentDataSource{}
}

type documentDataSource struct{}

// documentModel is the Terraform config/state model.
type documentModel struct {
	ID    types.String `tfsdk:"id"`
	Rules []ruleModel  `tfsdk:"rule"`
	JSON  types.String `tfsdk:"json"`
}

type ruleModel struct {
	Name        types.String      `tfsdk:"name"`
	Access      types.String      `tfsdk:"access"`
	Operations  types.List        `tfsdk:"operations"`
	Targets     types.List        `tfsdk:"targets"`
	Constraints []constraintModel `tfsdk:"constraint"`
}

type constraintModel struct {
	Operator types.String `tfsdk:"operator"`
	Key      types.String `tfsdk:"key"`
	Values   types.List   `tfsdk:"values"`
}

func (d *documentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_policy_document"
}

func (d *documentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Composes a Frostmoln IAM access-policy document from `rule` blocks " +
			"and exposes it as `json`, ready to assign to a `frostmoln_iam_policy` resource's `document`. " +
			"Native vocabulary: rule / access / operations / targets / constraints / FRN. This is a pure " +
			"local computation — it makes no API call; the server validates operations against the " +
			"append-only catalog when the policy is created.\n\n" +
			"Evaluation is default-deny; an explicit `deny` overrides any `allow`. Note that a " +
			"`constraint` on a **`deny`** rule *narrows* the deny — the deny only fires when the " +
			"constraint holds, so the operation stays permitted whenever it does not. To forbid an " +
			"operation unconditionally, use a `deny` rule with no `constraint`; to restrict an `allow` " +
			"(e.g. to a source-IP range), put the `constraint` on the `allow` rule.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "A content hash of the generated document.",
				Computed:    true,
			},
			"json": schema.StringAttribute{
				Description: "The composed access-policy document as a JSON string.",
				Computed:    true,
			},
		},
		Blocks: map[string]schema.Block{
			"rule": schema.ListNestedBlock{
				Description: "An access-policy rule. A rule matches only when its operation pattern, " +
					"its target FRN pattern, and every constraint hold. At least one rule is required.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "An optional human label for the rule (surfaced by the policy tester).",
							Optional:    true,
						},
						"access": schema.StringAttribute{
							Description: "`allow` grants the matched operation on the matched targets; " +
								"`deny` forbids it. An explicit deny overrides any allow.",
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf("allow", "deny"),
							},
						},
						"operations": schema.ListAttribute{
							Description: "One or more `service:resource:action` operation patterns with " +
								"per-segment `*` (e.g. `compute:instances:create`, `compute:*`, `*:*:delete`).",
							Required:    true,
							ElementType: types.StringType,
							Validators: []validator.List{
								listvalidator.SizeAtLeast(1),
							},
						},
						"targets": schema.ListAttribute{
							Description: "One or more FRN target patterns with per-segment `*` " +
								"(e.g. `frn:compute:*:*:instances/*`, `*`). The region segment must be " +
								"`*` — region-scoped targets are not supported yet.",
							Required:    true,
							ElementType: types.StringType,
							Validators: []validator.List{
								listvalidator.SizeAtLeast(1),
								listvalidator.ValueStringsAre(wildcardRegionTarget{}),
							},
						},
					},
					Blocks: map[string]schema.Block{
						"constraint": schema.ListNestedBlock{
							Description: "An optional constraint. `ipInRange` takes one or more CIDRs; " +
								"every other operator takes exactly one value.",
							NestedObject: schema.NestedBlockObject{
								Attributes: map[string]schema.Attribute{
									"operator": schema.StringAttribute{
										Description: "One of `equals`, `notEquals`, `like`, `ipInRange`, `before`, `after`.",
										Required:    true,
										Validators: []validator.String{
											stringvalidator.OneOf(constraintOperators...),
										},
									},
									"key": schema.StringAttribute{
										Description: "The constraint key, e.g. `frn:sourceIp`, `frn:currentTime`, " +
											"`frn:requestTag/<k>`, `frn:resourceTag/<k>`. `frn:region` is part of the key " +
											"vocabulary but is **not usable yet**: it evaluates as unknown on every request, " +
											"so an `allow` carrying it never grants and a `deny` carrying it fires in every " +
											"region. There is currently no way to scope a policy to a region.",
										Required: true,
									},
									"values": schema.ListAttribute{
										Description: "The constraint value(s). Exactly one for every operator " +
											"except `ipInRange`, which accepts a list of CIDRs.",
										Required:    true,
										ElementType: types.StringType,
										Validators: []validator.List{
											listvalidator.SizeAtLeast(1),
										},
									},
								},
							},
						},
					},
				},
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
			},
		},
	}
}

var _ validator.String = wildcardRegionTarget{}

// wildcardRegionTarget rejects a target FRN that pins the region segment of
// `frn:<service>:<region>:<tenant>:<type>/<id>` to anything but `*`. No service
// resolves a customer region, so a region-pinned target matches every request in
// the pre-handler check and no request once the resource has loaded — a `deny`
// written that way silently stops protecting. The server rejects it at create
// time; catching it here turns a failed apply into a failed plan.
//
// The split is SplitN(..., 5), matching the server: the trailing
// `<type>/<id>` field may legitimately contain colons (an object FRN is
// `frn:storage:*:t-abc:object/bucket:key`), so a plain Split would count 6+
// fields and wave that target through to a failed apply. Anything that does not
// resolve to 5 fields is left to the server, which owns FRN shape validation.
//
// Not expressible as stringvalidator.RegexMatches: the rule is a negation and
// Go's RE2 has no negative lookahead.
type wildcardRegionTarget struct{}

func (wildcardRegionTarget) Description(_ context.Context) string {
	return "target FRN must use `*` for the region segment"
}

func (v wildcardRegionTarget) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (wildcardRegionTarget) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	fields := strings.SplitN(req.ConfigValue.ValueString(), ":", 5)
	if len(fields) != 5 || fields[2] == "*" {
		return
	}
	resp.Diagnostics.AddAttributeError(
		req.Path,
		"Region-pinned policy target",
		fmt.Sprintf(
			"Target %q names region %q, but a region-pinned target cannot be matched: it matches every "+
				"request before the resource is loaded and none afterwards, so a deny written this way "+
				"stops protecting. Region-scoped targets are not supported yet — use * for the region "+
				"segment, e.g. frn:compute:*:*:instances/*.",
			req.ConfigValue.ValueString(), fields[2],
		),
	)
}

func (d *documentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config documentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rules := make([]ruleInput, 0, len(config.Rules))
	for i := range config.Rules {
		r := config.Rules[i]

		var ops, targets []string
		resp.Diagnostics.Append(r.Operations.ElementsAs(ctx, &ops, false)...)
		resp.Diagnostics.Append(r.Targets.ElementsAs(ctx, &targets, false)...)

		cons := make([]constraintInput, 0, len(r.Constraints))
		for j := range r.Constraints {
			c := r.Constraints[j]
			var vals []string
			resp.Diagnostics.Append(c.Values.ElementsAs(ctx, &vals, false)...)
			cons = append(cons, constraintInput{
				Operator: c.Operator.ValueString(),
				Key:      c.Key.ValueString(),
				Values:   vals,
			})
		}

		rules = append(rules, ruleInput{
			Name:        r.Name.ValueString(),
			Access:      r.Access.ValueString(),
			Operations:  ops,
			Targets:     targets,
			Constraints: cons,
		})
	}
	if resp.Diagnostics.HasError() {
		return
	}

	doc, err := buildPolicyJSON(rules)
	if err != nil {
		resp.Diagnostics.AddError("Invalid access policy", err.Error())
		return
	}

	sum := sha256.Sum256([]byte(doc))
	config.JSON = types.StringValue(doc)
	config.ID = types.StringValue(hex.EncodeToString(sum[:]))
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// --- pure composition (no Terraform plumbing, unit-tested directly) ---

type ruleInput struct {
	Name        string
	Access      string
	Operations  []string
	Targets     []string
	Constraints []constraintInput
}

type constraintInput struct {
	Operator string
	Key      string
	Values   []string
}

// docRule / policyDoc are the wire shape emitted (servicekit authz.Rule /
// AccessPolicy). Kept local — the provider does not depend on servicekit.
//
// The JSON tags MUST stay byte-congruent with servicekit authz.Rule /
// AccessPolicy: identity canonicalizes a stored policy by re-marshalling it
// through authz.AccessPolicy, and frostmoln_iam_policy suppresses a diff only
// when the composed document is semantically equal to that canonical form. In
// particular `name` is NOT omitempty (authz.Rule.Name is `json:"name"`), so an
// unnamed rule emits `"name":""` exactly as the server does — omitting it would
// make an unnamed rule fail "provider produced inconsistent result after apply".
type docRule struct {
	Name        string                    `json:"name"`
	Access      string                    `json:"access"`
	Operations  []string                  `json:"operations"`
	Targets     []string                  `json:"targets"`
	Constraints map[string]map[string]any `json:"constraints,omitempty"`
}

type policyDoc struct {
	SchemaVersion string    `json:"schemaVersion"`
	Rules         []docRule `json:"rules"`
}

// buildPolicyJSON composes the rules into an access-policy JSON document. It
// honours the frozen value-shape contract (servicekit authz.validConstraintKeyValue):
// ipInRange carries a JSON array of CIDRs; every other operator carries a single
// scalar string, so a scalar operator given more than one value is a hard error
// here rather than a server-side 400 after apply.
func buildPolicyJSON(rules []ruleInput) (string, error) {
	out := policyDoc{SchemaVersion: schemaVersion, Rules: make([]docRule, 0, len(rules))}
	for _, r := range rules {
		dr := docRule{
			Name:       r.Name,
			Access:     r.Access,
			Operations: r.Operations,
			Targets:    r.Targets,
		}
		if len(r.Constraints) > 0 {
			cons := map[string]map[string]any{}
			for _, c := range r.Constraints {
				if cons[c.Operator] == nil {
					cons[c.Operator] = map[string]any{}
				}
				if _, dup := cons[c.Operator][c.Key]; dup {
					return "", fmt.Errorf("duplicate constraint %s/%s in rule %q", c.Operator, c.Key, r.Name)
				}
				if c.Operator == "ipInRange" {
					cons[c.Operator][c.Key] = c.Values
				} else {
					if len(c.Values) != 1 {
						return "", fmt.Errorf("constraint operator %q takes exactly one value, got %d (rule %q)", c.Operator, len(c.Values), r.Name)
					}
					cons[c.Operator][c.Key] = c.Values[0]
				}
			}
			dr.Constraints = cons
		}
		out.Rules = append(out.Rules, dr)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
