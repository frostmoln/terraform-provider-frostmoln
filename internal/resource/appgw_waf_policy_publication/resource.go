// Package appgw_waf_policy_publication implements the
// frostmoln_appgw_waf_policy_publication Terraform resource.
//
// This resource exists to solve an ordering problem that `depends_on` solves
// badly. A WAF ruleset is authored as many independent rule and exclusion
// resources and then published as one atomic act; the publish must happen AFTER
// every rule write and must re-run EXACTLY when one of them changed. Writing
// that with depends_on means every practitioner remembering to list every rule,
// and a forgotten one produces a half-published policy with no error.
//
// Instead: every rule and exclusion exports a server-bumped `revision`, and
// this resource takes them as an input map. Any rule change changes an input,
// so Terraform's own dependency graph orders the publish last and re-runs it
// precisely when something changed. Nothing to remember, and a rule that is not
// listed is visibly not listed.
package appgw_waf_policy_publication

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource               = &publicationResource{}
	_ resource.ResourceWithConfigure  = &publicationResource{}
	_ resource.ResourceWithModifyPlan = &publicationResource{}
)

// Dry-run waits. VARIABLES, not constants, so a test can shrink them: with the
// real values a test that exercises the never-completes path runs for five
// minutes, which means in practice it is never written -- and that path is the
// one an operator actually hits today.
var (
	dryRunPollInterval = 3 * time.Second
	dryRunTimeout      = 5 * time.Minute
)

// PublicationModel is the Terraform state model for a WAF publication.
type PublicationModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	PolicyID  types.String `tfsdk:"policy_id"`

	RuleRevisions   types.Map   `tfsdk:"rule_revisions"`
	MaxNewlyBlocked types.Int64 `tfsdk:"max_newly_blocked"`

	Version     types.Int64  `tfsdk:"version"`
	ContentHash types.String `tfsdk:"content_hash"`

	// PlatformOptOuts names the platform protections this published ruleset
	// turns off. Computed, because they are authored outside Terraform.
	PlatformOptOuts types.List `tfsdk:"platform_opt_outs"`

	DryRunID              types.String `tfsdk:"dry_run_id"`
	DryRunNewlyBlocked    types.Int64  `tfsdk:"dry_run_newly_blocked"`
	DryRunNewlyAllowed    types.Int64  `tfsdk:"dry_run_newly_allowed"`
	DryRunRequestsSampled types.Int64  `tfsdk:"dry_run_requests_sampled"`

	PublishedAt types.String `tfsdk:"published_at"`
}

type apiDryRunSample struct {
	Method          string `json:"method"`
	Host            string `json:"host"`
	Path            string `json:"path"`
	MatchedSecRule  int    `json:"matchedSecRuleId"`
	MatchedRuleKey  string `json:"matchedRuleKey,omitempty"`
	OccurrenceCount int    `json:"occurrenceCount"`
}

type apiDryRun struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	Status      string `json:"status"`

	RequestsSampled int `json:"requestsSampled"`
	NewlyBlocked    int `json:"newlyBlocked"`
	NewlyAllowed    int `json:"newlyAllowed"`
	ElapsedMs       int `json:"elapsedMs"`

	Sample []apiDryRunSample `json:"sample,omitempty"`
	Error  string            `json:"error,omitempty"`
}

type apiDryRunListResponse struct {
	DryRuns []apiDryRun `json:"dryRuns"`
}

// apiDraft is the policy's draft as the server reports it: the fields that say
// whether it differs from what is enforced, plus the rules -- because one class
// of rule in there is security state that NOBODY'S HCL contains.
type apiDraft struct {
	Version struct {
		Version               int    `json:"version"`
		ContentHash           string `json:"contentHash"`
		HasUnpublishedChanges bool   `json:"hasUnpublishedChanges"`
	} `json:"version"`
	Rules []apiRule `json:"rules"`
}

// apiRule is the slice of a rule this resource needs to spot a platform opt-out.
type apiRule struct {
	RuleKey  string `json:"ruleKey"`
	Owner    string `json:"owner"`
	OptedOut bool   `json:"optedOut"`
}

// platformOptOuts returns the keys of platform-owned rules this ruleset turns
// OFF, sorted.
//
// 🔴 THIS IS THE ONE PIECE OF PUBLISHED SECURITY STATE THAT APPEARS IN NO HCL.
// frostmoln_appgw_waf_rule correctly refuses to manage a platform rule and
// points at the CLI or the portal -- and both of those write the DRAFT, which
// changes the content hash, which makes this resource's ModifyPlan see
// hasUnpublishedChanges. So the next `terraform apply` publishes a ruleset that
// disables a platform protection nobody in this configuration chose, and the
// plan says only "version will be known after apply".
//
// The asymmetry cannot be closed here -- Terraform does not own the opt-out --
// but it can be made VISIBLE, which is why fm's export format carries
// platformOptOuts as a first-class section rather than folding it into rules.
func platformOptOuts(rules []apiRule) []string {
	var keys []string
	for _, r := range rules {
		if r.Owner == "platform" && r.OptedOut {
			keys = append(keys, r.RuleKey)
		}
	}
	sort.Strings(keys)
	return keys
}

type apiVersion struct {
	Version     int    `json:"version"`
	State       string `json:"state"`
	ContentHash string `json:"contentHash"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

type publicationResource struct {
	client *client.Client
}

// NewResource returns a new WAF publication resource factory.
func NewResource() resource.Resource {
	return &publicationResource{}
}

func (r *publicationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_waf_policy_publication"
}

func (r *publicationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Runs a dry-run and publishes an Application Gateway WAF policy's draft, making " +
			"it the version the gateway enforces.\n\n" +
			"## Ordering without `depends_on`\n\n" +
			// 🔴 NO FENCED CODE BLOCK IN A SCHEMA DESCRIPTION. tfplugindocs copies
			// the description into the page's YAML front matter and indents every
			// line by two spaces — which turns a blank line INSIDE a code block
			// into a whitespace-only line, and the trailing-whitespace hook fails
			// the build. Blank lines BETWEEN paragraphs are collapsed and are
			// fine; only the ones a code block preserves survive to bite.
			//
			// The example belongs in examples/resources/<type>/resource.tf
			// anyway: tfplugindocs renders it into the page's Example Usage
			// section, so duplicating it here published the same HCL twice.
			"Feed every rule's and exclusion's `revision` into `rule_revisions` — see the example " +
			"below. Because the revisions are server-assigned and bump on every write, any rule " +
			"change changes an input here, so Terraform's own graph orders this last and re-runs " +
			"it exactly when something changed.\n\n" +
			"## The dry-run is a gate, not advice\n\n" +
			"Applying this resource starts a dry-run, waits for it, and refuses to publish if more " +
			"than `max_newly_blocked` requests in the sample would newly be refused — printing the " +
			"request shapes that would break. The server independently refuses any publish that has " +
			"no completed dry-run matching the draft exactly, settings included.\n\n" +
			"## Publishing is not applying\n\n" +
			"A published version reaches the appliance on the gateway's next configuration apply. " +
			"This resource publishes; it does not dispatch.\n\n" +
			"~> **Declare at most one publication per policy.** A second one targeting the same " +
			"`policy_id` is not refused, but both then compute the same `version` and `id`, and " +
			"each apply races the other's dry-run. One policy, one publication.\n\n" +
			"## Platform opt-outs reach this resource from outside Terraform\n\n" +
			"`frostmoln_appgw_waf_rule` will not manage a **platform-owned** rule — the tenant may " +
			"only opt out of one, and only where the platform allows it, using the CLI or the " +
			"portal. Both of those write the **draft**, which changes its content hash, which this " +
			"resource sees as `hasUnpublishedChanges`. So the next apply publishes that opt-out: a " +
			"platform protection is switched off by a change that appears in **no HCL**.\n\n" +
			"This provider cannot own that decision, so it makes it visible instead. The plan warns " +
			"and names each rule, and `platform_opt_outs` records what the published ruleset " +
			"disables. Read them: an opt-out is normally a false-positive workaround for an " +
			"emergency virtual patch, and it is the single most consequential edit a tenant can " +
			"make to a WAF.\n\n" +
			"~> **Destroying this resource does not unpublish anything.** A published version stays " +
			"published — there is no unpublish, by design, because history is never rewritten. " +
			"Destroy removes it from state and warns; use a rollback to go back to an earlier version.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The identifier of this publication: the policy id and the version it published.",
				Computed:    true,
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway the policy belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"policy_id": schema.StringAttribute{
				Description:   "The WAF policy to publish.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"rule_revisions": schema.MapAttribute{
				Description: "A map of rule key to that rule's `revision`. This is the ordering " +
					"input: its only job is to change when a rule changes.",
				Required:    true,
				ElementType: types.Int64Type,
			},
			"max_newly_blocked": schema.Int64Attribute{
				Description: "Refuse to publish if the dry-run reports more than this many requests " +
					"in its sample would NEWLY be blocked.\n\n" +
					"**Defaults to `0`** — publish nothing that breaks anything in the sample. That " +
					"is the right setting for a change meant to be invisible, and it is the default " +
					"because a safety control that is off unless you opt in is one most " +
					"configurations will not have.\n\n" +
					"Raise it deliberately when a rule is *supposed* to start blocking. There is no " +
					"way to disable the check entirely: set a number large enough to cover what you " +
					"intend, and the dry-run's figures are recorded either way.",
				Optional:   true,
				Computed:   true,
				Default:    int64default.StaticInt64(0),
				Validators: []validator.Int64{int64validator.AtLeast(0)},
			},
			"version": schema.Int64Attribute{
				Description: "The version number this publication produced.",
				Computed:    true,
			},
			"content_hash": schema.StringAttribute{
				Description: "The canonical hash of the rules, exclusions and settings that were published.",
				Computed:    true,
			},
			"dry_run_id": schema.StringAttribute{
				Description: "The dry-run that gated this publication.",
				Computed:    true,
			},
			"dry_run_newly_blocked": schema.Int64Attribute{
				Description: "How many requests in the dry-run's sample would NEWLY be refused by " +
					"the published ruleset. The number that says whether this change breaks someone.",
				Computed: true,
			},
			"dry_run_newly_allowed": schema.Int64Attribute{
				Description: "How many requests in the dry-run's sample would newly be allowed.",
				Computed:    true,
			},
			"dry_run_requests_sampled": schema.Int64Attribute{
				Description: "How many requests the dry-run replayed. A small sample means a weak signal.",
				Computed:    true,
			},
			"platform_opt_outs": schema.ListAttribute{
				Description: "The keys of platform-owned rules that the published ruleset turns " +
					"**off** for this gateway.\n\n" +
					"These are tenant-authored security decisions that live outside Terraform: " +
					"`frostmoln_appgw_waf_rule` will not manage a platform rule, so an opt-out is " +
					"made with the CLI or the portal. Both write the draft, so the next apply of " +
					"this resource publishes it — and without this attribute the plan would say " +
					"only \"version will be known after apply\" while disabling a protection that " +
					"appears in nobody's configuration. Anything listed here is a protection you " +
					"are choosing not to run.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"published_at": schema.StringAttribute{
				Description: "When the version was published.",
				Computed:    true,
			},
		},
	}
}

// ModifyPlan makes an unpublished draft VISIBLE IN THE PLAN.
//
// 🔴 WITHOUT THIS THE RESOURCE MISSES THE TWO CHANGES THAT MATTER MOST.
// `rule_revisions` only moves when a rule this configuration still declares is
// written, so on its own it is blind to:
//
//   - a POLICY SETTING. Flipping frostmoln_appgw_waf_policy.mode to "block"
//     changes no revision, so the publication plans nothing, and the policy
//     reads "block" while the gateway carries on detecting. That is the single
//     thing an operator must never be wrong about.
//   - a rule DELETION. Terraform orders a dependent's update BEFORE the
//     dependency's destroy, so the publish would freeze a draft that still
//     contains the rule, and the next plan sees a revision map that already
//     matches state. The deleted rule stays enforced indefinitely.
//
// It also catches drift: an out-of-band rollback or portal edit leaves the
// draft differing from the active version, and the plan now says so instead of
// reporting agreement.
//
// Reading the draft is a GET, so this stays a side-effect-free plan. The
// dry-run is emphatically NOT run here.
func (r *publicationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan PublicationModel
	if diags := req.Plan.Get(ctx, &plan); diags.HasError() {
		return
	}
	// The client is nil during the framework's early plan walks.
	if r.client == nil || plan.GatewayID.IsUnknown() || plan.PolicyID.IsUnknown() {
		return
	}

	base := r.policyPath(plan.GatewayID.ValueString(), plan.PolicyID.ValueString())
	apiResp, err := r.client.Get(ctx, base+"/draft", nil)
	if err != nil {
		// A policy that does not exist yet is the create case, not a problem.
		return
	}
	draft, err := client.ParseResponse[apiDraft](apiResp)
	if err != nil {
		return
	}
	if !draft.Version.HasUnpublishedChanges {
		return
	}

	// There IS something to publish. Mark the outputs unknown so the plan shows
	// this resource changing, which is what makes Update run.
	resp.Plan.SetAttribute(ctx, path.Root("version"), types.Int64Unknown())
	resp.Plan.SetAttribute(ctx, path.Root("content_hash"), types.StringUnknown())
	resp.Plan.SetAttribute(ctx, path.Root("published_at"), types.StringUnknown())
	resp.Plan.SetAttribute(ctx, path.Root("dry_run_id"), types.StringUnknown())
	resp.Plan.SetAttribute(ctx, path.Root("dry_run_newly_blocked"), types.Int64Unknown())
	resp.Plan.SetAttribute(ctx, path.Root("dry_run_newly_allowed"), types.Int64Unknown())
	resp.Plan.SetAttribute(ctx, path.Root("dry_run_requests_sampled"), types.Int64Unknown())
	resp.Plan.SetAttribute(ctx, path.Root("platform_opt_outs"), types.ListUnknown(types.StringType))

	// 🔴 NAME THE PLATFORM PROTECTIONS THIS APPLY WOULD SWITCH OFF.
	//
	// An opt-out is made with the CLI or the portal -- this provider's rule
	// resource refuses platform rules by design -- and it writes the DRAFT. So
	// it arrives here as hasUnpublishedChanges, and applying publishes it. The
	// plan would otherwise report "version will be known after apply" for a
	// change whose actual content is "stop running an emergency virtual patch",
	// which is the single most consequential edit a tenant can make and the one
	// no HCL diff can show.
	if optOuts := platformOptOuts(draft.Rules); len(optOuts) > 0 {
		resp.Diagnostics.AddWarning("This Apply Will Publish Platform Protections That Are Turned OFF",
			fmt.Sprintf("The draft opts out of %d platform-owned rule(s), and applying this "+
				"resource publishes them:\n\n  %s\n\nThese are decisions made outside "+
				"Terraform — frostmoln_appgw_waf_rule will not manage a platform rule, so an "+
				"opt-out is made with the CLI or the portal, and it lands on the same draft this "+
				"resource publishes. Each one is a protection this gateway will stop running. If "+
				"that is not intended, re-enable it before applying.",
				len(optOuts), strings.Join(optOuts, "\n  ")))
	}

	if req.State.Raw.IsNull() {
		return // create: the plan already shows everything as unknown
	}
	var state PublicationModel
	if diags := req.State.Get(ctx, &state); diags.HasError() {
		return
	}
	if !state.DryRunNewlyBlocked.IsNull() && state.DryRunNewlyBlocked.ValueInt64() > 0 {
		resp.Diagnostics.AddWarning("The Last Publication Newly Blocked Traffic",
			fmt.Sprintf("The previous apply published a ruleset whose dry-run reported %d "+
				"request(s) in its sample would newly be refused. This apply will run a fresh "+
				"dry-run and report the current figure; set max_newly_blocked to refuse a "+
				"publication that would break more than you intend.",
				state.DryRunNewlyBlocked.ValueInt64()))
	}
}

func (r *publicationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *publicationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PublicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.publish(ctx, &plan, &resp.Diagnostics)
	// 🔴 STATE IS WRITTEN WHENEVER A VERSION WAS PUBLISHED, EVEN ON AN ERROR.
	// Some refusals happen AFTER the publish landed — the replayed-hash check
	// is one — and returning without writing would leave a frozen, enforced
	// version that Terraform has never heard of. A failed apply the
	// practitioner must act on is right; losing the record of what is being
	// enforced is not.
	if publishedSomething(&plan) || !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *publicationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PublicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.publish(ctx, &plan, &resp.Diagnostics)
	if publishedSomething(&plan) || !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

// publishedSomething reports whether a version actually reached the policy, so
// a post-publish refusal still records what is enforced.
func publishedSomething(m *PublicationModel) bool {
	return !m.Version.IsNull() && !m.Version.IsUnknown()
}

// publish runs the whole gate: dry-run, budget check, publish.
func (r *publicationResource) publish(ctx context.Context, m *PublicationModel, diags diagnosticsSink) {
	base := r.policyPath(m.GatewayID.ValueString(), m.PolicyID.ValueString())

	dr, err := r.runDryRun(ctx, base)
	if err != nil {
		diags.AddError("WAF Dry Run Failed", err.Error())
		return
	}
	m.DryRunID = types.StringValue(dr.ID)
	m.DryRunNewlyBlocked = types.Int64Value(int64(dr.NewlyBlocked))
	m.DryRunNewlyAllowed = types.Int64Value(int64(dr.NewlyAllowed))
	m.DryRunRequestsSampled = types.Int64Value(int64(dr.RequestsSampled))

	// 🔴 A ZERO-SAMPLE DRY-RUN MEASURES NOTHING, so it cannot satisfy a budget.
	// A gateway with no recent traffic — freshly built, or whose appliance was
	// just replaced — produces a completed dry-run with 0 sampled and 0 newly
	// blocked, and `0 > 0` is false. Without this, max_newly_blocked = 0 would
	// wave through a ruleset that refuses every request the site actually
	// serves.
	if !m.MaxNewlyBlocked.IsNull() && !m.MaxNewlyBlocked.IsUnknown() && dr.RequestsSampled == 0 {
		diags.AddError("The Dry Run Replayed No Traffic, So The Budget Could Not Be Checked",
			"max_newly_blocked is set, but the dry-run sampled 0 requests — it has nothing to "+
				"measure this ruleset against, so passing the budget means nothing.\n\n"+
				"A gateway accumulates the request signatures a replay uses only once it is serving "+
				"traffic. Check that the gateway is running and has applied a configuration; if it "+
				"is genuinely new, publish once without max_newly_blocked and set it before the "+
				"next change.")
		return
	}

	// The budget check, before anything is published.
	if !m.MaxNewlyBlocked.IsNull() && !m.MaxNewlyBlocked.IsUnknown() {
		if max := m.MaxNewlyBlocked.ValueInt64(); int64(dr.NewlyBlocked) > max {
			diags.AddError("Publication Refused: Too Many Requests Would Newly Be Blocked",
				fmt.Sprintf("The dry-run replayed %d requests and found %d that this ruleset would "+
					"newly refuse, which is more than max_newly_blocked = %d. Nothing was published.\n\n%s\n\n"+
					"Either narrow the rules (an exclusion is the usual fix), or raise "+
					"max_newly_blocked if this ruleset is meant to start blocking.",
					dr.RequestsSampled, dr.NewlyBlocked, max, formatSample(dr.Sample)))
			return
		}
	}
	if dr.NewlyBlocked > 0 {
		diags.AddWarning("This Publication Newly Blocks Traffic",
			fmt.Sprintf("%d of the %d requests replayed would newly be refused.\n\n%s",
				dr.NewlyBlocked, dr.RequestsSampled, formatSample(dr.Sample)))
	}

	apiResp, err := r.client.Post(ctx, base+"/publish", nil)
	if err != nil {
		// 🔴 IN THIS RESOURCE A 409 CANNOT MEAN "YOU FORGOT TO DRY-RUN".
		//
		// The server refuses a publish with 409 when no completed dry-run
		// matches the draft's content hash, and for a human at a CLI the
		// actionable advice is "run one". Here it is not: this resource ran a
		// dry-run seconds ago, on this same draft, and waited for it. The hash
		// can therefore only have stopped matching because SOMETHING ELSE moved
		// the draft in between -- a second apply, the portal, the CLI -- which
		// is the same race the replayed-hash check below catches on the other
		// side of the publish. Repeating the generic advice would send a
		// practitioner to re-run the thing that is already running.
		if client.IsConflict(err) {
			diags.AddError("The Draft Changed While This Apply Was Publishing It",
				fmt.Sprintf("The server refused the publish: no completed dry-run matches the "+
					"draft any more. This apply ran a dry-run (%s) against the draft moments ago "+
					"and it completed, so the draft has been edited since — by a concurrent "+
					"apply, the portal, or the CLI.\n\nNothing was published. Re-run to "+
					"dry-run and publish the current draft, and check whether another writer is "+
					"racing this configuration.\n\nUnderlying error: %s", dr.ID, err.Error()))
			return
		}
		diags.AddError("Failed to Publish WAF Policy", err.Error())
		return
	}

	// 204 means the draft is byte-identical to what is already active: a
	// successful no-op, and what makes a repeated apply idempotent instead of
	// cutting a version nobody asked for. The active version is read back so
	// state still names what is enforced.
	if apiResp.StatusCode == 204 {
		v, optOuts, verr := r.activeVersion(ctx, base)
		if verr != nil {
			diags.AddError("Failed to Read The Active WAF Version", verr.Error())
			return
		}
		m.applyVersion(v)
		m.PlatformOptOuts = listOfStrings(ctx, optOuts)
		return
	}

	v, err := client.ParseResponse[apiVersion](apiResp)
	if err != nil {
		diags.AddError("Failed to Parse WAF Publish Response", err.Error())
		return
	}
	// 🔴 THE VERSION PUBLISHED MUST BE THE ONE THAT WAS REPLAYED.
	//
	// The server's gate accepts ANY completed dry-run whose hash matches the
	// draft at publish time — not specifically the one this apply started. So a
	// concurrent editor (a second apply, the portal, the CLI) can move the
	// draft to a different ruleset and dry-run it, and this publish then ships
	// content that max_newly_blocked never measured. Without this check the
	// resource would record one dry-run's numbers beside another ruleset's
	// hash, attesting to a measurement that does not describe what it published.
	if dr.ContentHash != "" && v.ContentHash != "" && dr.ContentHash != v.ContentHash {
		diags.AddError("Published A Ruleset That Was Not The One Replayed",
			fmt.Sprintf("The dry-run measured content hash %s, but version %d was published with "+
				"hash %s — the draft changed between the two, so another change landed during this "+
				"apply.\n\nThe published version was NOT checked against max_newly_blocked. "+
				"Re-run to dry-run and publish the current draft, and check whether a concurrent "+
				"apply or an out-of-band edit is racing this one.", dr.ContentHash, v.Version, v.ContentHash))
		// State is still written: the version IS published, and leaving
		// Terraform unaware of it would be worse than a failed apply.
		m.applyVersion(v)
		r.recordPublishedOptOuts(ctx, base, m, diags)
		return
	}
	m.applyVersion(v)
	r.recordPublishedOptOuts(ctx, base, m, diags)
}

// recordPublishedOptOuts reads the now-enforcing version to record which
// platform protections it turns off.
//
// A failure here is NOT a failed publish -- the version is live either way --
// so it warns rather than erroring, and leaves the attribute empty.
func (r *publicationResource) recordPublishedOptOuts(ctx context.Context, base string, m *PublicationModel, diags diagnosticsSink) {
	_, optOuts, err := r.activeVersion(ctx, base)
	if err != nil {
		diags.AddWarning("Could Not Read The Published Ruleset's Platform Opt-Outs",
			fmt.Sprintf("The version was published. Reading back which platform-owned rules it "+
				"turns off failed: %s", err.Error()))
		m.PlatformOptOuts = types.ListValueMust(types.StringType, []attr.Value{})
		return
	}
	if len(optOuts) > 0 {
		diags.AddWarning("The Published Ruleset Turns Platform Protections OFF",
			fmt.Sprintf("Version %d disables %d platform-owned rule(s):\n\n  %s\n\nThese "+
				"opt-outs were authored outside Terraform (the CLI or the portal) and have now "+
				"been published.", m.Version.ValueInt64(), len(optOuts), strings.Join(optOuts, "\n  ")))
	}
	m.PlatformOptOuts = listOfStrings(ctx, optOuts)
}

// listOfStrings renders keys as a list value, empty rather than null when there
// are none: "this ruleset disables nothing" is a fact, and null reads as "not
// looked at".
func listOfStrings(ctx context.Context, keys []string) types.List {
	if len(keys) == 0 {
		return types.ListValueMust(types.StringType, []attr.Value{})
	}
	v, d := types.ListValueFrom(ctx, types.StringType, keys)
	if d.HasError() {
		return types.ListValueMust(types.StringType, []attr.Value{})
	}
	return v
}

func (m *PublicationModel) applyVersion(v *apiVersion) {
	m.Version = types.Int64Value(int64(v.Version))
	m.ContentHash = types.StringValue(v.ContentHash)
	m.PublishedAt = types.StringValue(v.PublishedAt)
	m.ID = types.StringValue(fmt.Sprintf("%s/%d", m.PolicyID.ValueString(), v.Version))
}

// runDryRun starts a replay and waits for a verdict.
func (r *publicationResource) runDryRun(ctx context.Context, base string) (*apiDryRun, error) {
	apiResp, err := r.client.Post(ctx, base+"/dry-runs", nil)
	if err != nil {
		return nil, err
	}
	started, err := client.ParseResponse[apiDryRun](apiResp)
	if err != nil {
		return nil, err
	}
	if started.Status == "completed" {
		return started, nil
	}

	deadline := time.Now().Add(dryRunTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(dryRunPollInterval):
		}
		latest, err := r.findDryRun(ctx, base, started.ID)
		if err != nil {
			return nil, err
		}
		if latest == nil {
			continue
		}
		switch latest.Status {
		case "completed":
			return latest, nil
		case "failed":
			if latest.Error != "" {
				return nil, fmt.Errorf("the dry-run failed: %s", latest.Error)
			}
			return nil, fmt.Errorf("the dry-run failed")
		}
	}

	// 🔴 NAME THE ACTUAL CAUSE. The replay runs ON THE APPLIANCE, against
	// request signatures held inside the tenant's VPC. If this gateway's
	// appliance is not running the inspection engine, no dry-run can ever
	// complete and every publish is refused -- and "the dry-run did not
	// complete in 5m0s" leaves an operator debugging Terraform instead of the
	// gateway.
	return nil, fmt.Errorf(
		"the dry-run did not complete within %s.\n\n"+
			"A dry-run is replayed by the gateway's own appliance against recent request "+
			"signatures. It stays pending when the appliance is not running the inspection "+
			"engine, or when the gateway has not yet applied a configuration. Check the "+
			"gateway's config_status, and that it is running, before retrying", dryRunTimeout)
}

// findDryRun locates one dry-run in the policy's history.
func (r *publicationResource) findDryRun(ctx context.Context, base, id string) (*apiDryRun, error) {
	apiResp, err := r.client.Get(ctx, base+"/dry-runs", nil)
	if err != nil {
		return nil, err
	}
	list, err := client.ParseResponse[apiDryRunListResponse](apiResp)
	if err != nil {
		return nil, err
	}
	for i := range list.DryRuns {
		if list.DryRuns[i].ID == id {
			return &list.DryRuns[i], nil
		}
	}
	return nil, nil
}

// activeVersion reads the version being ENFORCED, and its rules with it.
//
// The rules are read because platform opt-outs are recorded on this resource:
// what matters is which protections the PUBLISHED ruleset turns off, and the
// enforcing version is the only object that answers that.
func (r *publicationResource) activeVersion(ctx context.Context, base string) (*apiVersion, []string, error) {
	apiResp, err := r.client.Get(ctx, base+"/versions/active", nil)
	if err != nil {
		return nil, nil, err
	}
	wrapper, err := client.ParseResponse[struct {
		Version apiVersion `json:"version"`
		Rules   []apiRule  `json:"rules"`
	}](apiResp)
	if err != nil {
		return nil, nil, err
	}
	return &wrapper.Version, platformOptOuts(wrapper.Rules), nil
}

// recordOptOuts records the published opt-outs on the model.
func (m *PublicationModel) recordOptOuts(ctx context.Context, keys []string, _ *diag.Diagnostics) {
	m.PlatformOptOuts = listOfStrings(ctx, keys)
}

func (r *publicationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PublicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The publication is an ACT, not a stored object: what exists afterwards is
	// a frozen version. Refreshing therefore means asking what is enforced now.
	// If the policy is gone, so is this.
	v, optOuts, err := r.activeVersion(ctx, r.policyPath(state.GatewayID.ValueString(), state.PolicyID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read The Active WAF Version", err.Error())
		return
	}
	// 🔴 DO NOT ADOPT A VERSION THIS RESOURCE DID NOT PUBLISH.
	//
	// version, content_hash and published_at are all Computed with no config
	// counterpart, so overwriting them from whatever is active would make
	// Terraform absorb it as this resource's own state — and the next plan,
	// seeing rule_revisions unchanged, would report no changes.
	//
	// The case that costs something: an operator rolls the policy back to an
	// earlier, more permissive version out of band (rollback is deliberately
	// exempt from the dry-run gate). The one resource whose purpose is to
	// assert "this ruleset is what is enforced" would report agreement with a
	// ruleset nobody in this configuration chose.
	//
	// Removing it from state instead makes the next plan show a create, which
	// re-runs dry-run + publish and restores the intended ruleset. That is the
	// fail-safe direction, and it is how the rest of this provider treats "the
	// remote object is no longer the one we manage".
	if !state.ContentHash.IsNull() && state.ContentHash.ValueString() != "" &&
		v.ContentHash != state.ContentHash.ValueString() {
		resp.Diagnostics.AddWarning("The Enforced WAF Version Is Not The One This Resource Published",
			fmt.Sprintf("This publication published content hash %s; the gateway is now enforcing "+
				"version %d with hash %s. Something published or rolled back outside this "+
				"configuration.\n\nThe resource has been removed from state so the next plan "+
				"re-publishes the ruleset your configuration describes. If the out-of-band change "+
				"was intended, update the configuration to match before applying.",
				state.ContentHash.ValueString(), v.Version, v.ContentHash))
		resp.State.RemoveResource(ctx)
		return
	}

	state.applyVersion(v)
	state.recordOptOuts(ctx, optOuts, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete removes the publication from state and says what it did not do.
//
// There is no unpublish, deliberately: history is never rewritten, and a
// version that has been enforced stays in the record. Erroring here would wedge
// `terraform destroy`; silently succeeding would imply the ruleset stopped
// being enforced. Saying so is the honest option.
func (r *publicationResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("The Published Version Is Still Enforced",
		"Destroying this resource removes the record of the publication from Terraform state. It "+
			"does not unpublish anything: the WAF version stays the one the gateway enforces, "+
			"because a published version is never withdrawn. To go back, publish an earlier "+
			"version's rules (a rollback), or destroy the policy itself.")
}

func (r *publicationResource) policyPath(gwID, policyID string) string {
	return r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/waf-policies/%s", gwID, policyID))
}

// formatSample renders the would-be-blocked shapes, most frequent first, so the
// error names the traffic that would break rather than only counting it.
func formatSample(sample []apiDryRunSample) string {
	if len(sample) == 0 {
		return "The dry-run returned no sample of the affected requests."
	}
	sorted := append([]apiDryRunSample(nil), sample...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OccurrenceCount > sorted[j].OccurrenceCount })
	var b strings.Builder
	b.WriteString("Requests that would newly be refused:\n")
	for i, s := range sorted {
		if i == 10 {
			fmt.Fprintf(&b, "  ... and %d more shapes\n", len(sorted)-10)
			break
		}
		rule := fmt.Sprintf("SecRule %d", s.MatchedSecRule)
		if s.MatchedRuleKey != "" {
			rule = s.MatchedRuleKey
		}
		fmt.Fprintf(&b, "  %4d x  %-6s %s%s  (matched %s)\n",
			s.OccurrenceCount, s.Method, s.Host, s.Path, rule)
	}
	return b.String()
}

// diagnosticsSink is the subset of diag.Diagnostics publish needs, so the same
// code serves Create and Update without either passing its whole response.
type diagnosticsSink interface {
	AddError(summary, detail string)
	AddWarning(summary, detail string)
	HasError() bool
}
