package secret

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/planmod"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/writeonly"
)

var (
	_ resource.Resource                     = &secretResource{}
	_ resource.ResourceWithImportState      = &secretResource{}
	_ resource.ResourceWithConfigValidators = &secretResource{}
	_ resource.ResourceWithModifyPlan       = &secretResource{}
)

// NewResource returns a new secret resource factory.
func NewResource() resource.Resource {
	return &secretResource{}
}

type secretResource struct {
	client *client.Client
}

// ConfigValidators keeps secret_value and its write-only twin mutually
// exclusive. secret_value was Required before secret_value_wo existed; relaxing
// it to Optional would otherwise let a config supply neither.
func (r *secretResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("secret_value"),
			path.MatchRoot("secret_value_wo"),
		),
	}
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a secret in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the secret.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the secret. Cannot be changed after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A description of the secret.",
				Optional:    true,
			},
			"secret_value": schema.StringAttribute{
				Description: "The secret value. Terraform persists every configured attribute, so this one is " +
					"stored in state in plaintext no matter where the value came from — minting it out of band " +
					"and passing it through a variable does not change that. Prefer `secret_value_wo`, which is " +
					"never written to state. Exactly one of `secret_value` or `secret_value_wo` must be set, " +
					"and the value must be at least one character: there is no clear-a-secret operation, so " +
					"an empty value is refused at plan time when it is known then, refused at apply time " +
					"when it is not, and rejected by current versions of the API. " +
					docs.StateSecretNote,
				Optional:  true,
				Sensitive: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.PreferWriteOnlyAttribute(path.MatchRoot("secret_value_wo")),
				},
			},
			"secret_value_wo": schema.StringAttribute{
				Description: "The secret value, as a [write-only argument]" +
					"(https://developer.hashicorp.com/terraform/language/resources/ephemeral/write-only): it " +
					"reaches the provider on apply and is never written to the plan or to state. Requires " +
					"Terraform 1.11 or later. Exactly one of `secret_value` or `secret_value_wo` must be set, " +
					"and `secret_value_wo_version` is required whenever this one is. The value must be at " +
					"least one character. " +
					"Because the value is not stored, Terraform can detect no change to it in either " +
					"direction: bump `secret_value_wo_version` to push a new value, and accept that a " +
					"secret rotated outside Terraform is invisible to `plan` and will not be corrected.",
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				Validators: []validator.String{
					// An empty value is not an update: there is no
					// clear-a-secret operation. secrets v0.12.23 rejects it with
					// a 400, but refusing at plan time is the better error and
					// does not depend on which server version answers.
					stringvalidator.LengthAtLeast(1),
					stringvalidator.AlsoRequires(path.MatchRoot("secret_value_wo_version")),
				},
			},
			"secret_value_wo_version": schema.StringAttribute{
				Description: "Change tracker for `secret_value_wo`, required whenever that attribute is set. " +
					"Any change to this value makes Terraform send the current `secret_value_wo` to the " +
					"platform as a new secret version; leaving it alone leaves the stored secret untouched, " +
					"however much the write-only value changes. Bumping it without changing the value still " +
					"writes a new version, which counts against `max_versions`. Its content is arbitrary — a " +
					"counter or a date is typical — and unlike the value it is stored in state, so do not " +
					"derive it from the secret or from anything in it: a digest of the secret is printed " +
					"verbatim in `terraform plan` output, and it is an offline confirmation oracle. " +
					"`terraform import` leaves this unset, so the first apply against an imported secret " +
					"writes a new version.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("secret_value_wo")),
				},
			},
			"content_type": schema.StringAttribute{
				Description: "The content type of the secret value. Defaults to \"text/plain\". Fixed when the " +
					"secret is created: the API's update accepts only the value, description and tags, so " +
					"changing this is refused at plan time rather than applied and quietly ignored.",
				Optional: true,
				Computed: true,
				// NOT a schema Default: TransformDefaults substitutes it whenever
				// the CONFIG value is null, overwriting the state value of an
				// imported secret and turning ModifyPlan into a permanent plan
				// error for a config the practitioner never changed.
				PlanModifiers: []planmodifier.String{
					planmod.StringUseStateOrDefault("text/plain"),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the secret.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"max_versions": schema.Int64Attribute{
				Description: "The maximum number of versions to retain. Defaults to 10. Fixed when the secret " +
					"is created: the API's update accepts only the value, description and tags, so changing " +
					"this is refused at plan time. Choose it at create — it cannot be raised later, and Vault " +
					"prunes against it.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					planmod.Int64UseStateOrDefault(10),
				},
			},
			"recovery_window_days": schema.Int64Attribute{
				Description: "The number of days to retain a deleted secret before permanent removal, and the " +
					"window during which the secret's name stays taken after a destroy. Defaults to 7. Fixed " +
					"when the secret is created: the API's update accepts only the value, description and " +
					"tags, so changing this is refused at plan time.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					planmod.Int64UseStateOrDefault(7),
				},
			},
			"current_version": schema.Int64Attribute{
				Description: "The current version number of the secret.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the secret.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the secret was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the secret was last updated.",
				Computed:    true,
			},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

// blankSecretValue is the apply-time backstop for the legacy secret_value.
//
// The schema's LengthAtLeast(1) self-disables on an unknown value, so a
// secret_value wired to another resource's output or a data source is not
// checked at plan time and only resolves during apply. secret_value_wo has the
// equivalent guard in writeonly.Attr.Read; this is the legacy half of it. There
// is no clear-a-secret operation, so a blank value is never an update: refuse it
// rather than let the request decide, which against a server without the secrets
// v0.12.23 guard writes a blank version and prunes history at max_versions.
func blankSecretValue(value string, diags *diag.Diagnostics) bool {
	if strings.TrimSpace(value) != "" {
		return false
	}
	diags.AddAttributeError(
		path.Root("secret_value"),
		"secret_value must not be empty",
		"The value resolved to an empty string during apply, so the plan-time length check "+
			"could not catch it. A secret value must be at least one character: there is no "+
			"clear-a-secret operation, and an empty value is not a way to blank one.",
	)
	return true
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A write-only attribute is null in the plan by construction; its value
	// only ever reaches the provider through the config.
	valueWO := secretValueWO.Read(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !valueWO.IsNull() {
		apiReq.SecretValue = valueWO.ValueString()
	} else if blankSecretValue(apiReq.SecretValue, &resp.Diagnostics) {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/secrets"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create secret", err.Error())
		return
	}

	s, err := client.ParseResponse[apiSecret](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	plan.fromAPI(ctx, s, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/secrets/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read secret", err.Error())
		return
	}

	s, err := client.ParseResponse[apiSecret](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	state.fromAPI(ctx, s, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SecretModel
	var state SecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	valueWO := secretValueWO.Read(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The plan-time refusal skips unknown values; by apply both sides are known,
	// and without this an interpolated max_versions would still reach a request
	// that discards it.
	if refused := refusedCreateTimeChanges(plan, state); len(refused) > 0 {
		addCreateTimeRefusals(&resp.Diagnostics, refused)
		return
	}

	id := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// secret_value_wo is null on both sides of the diff, so toUpdateRequest can
	// never see it change. The practitioner-set version companion is the only
	// signal that a new value should be sent.
	if !valueWO.IsNull() && !plan.SecretValueWOVer.Equal(state.SecretValueWOVer) {
		v := valueWO.ValueString()
		updateReq.SecretValue = &v
	} else if valueWO.IsNull() && updateReq.SecretValue != nil && // pragma: allowlist secret
		blankSecretValue(*updateReq.SecretValue, &resp.Diagnostics) {
		return
	}

	_, err := r.client.Put(ctx, r.client.TenantPath("/secrets/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update secret", err.Error())
		return
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/secrets/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read secret after update", err.Error())
		return
	}

	s, err := client.ParseResponse[apiSecret](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	plan.fromAPI(ctx, s, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/secrets/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete secret", err.Error())
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// createTimeAttr is one attribute the API fixes at create, with the values on
// both sides so the diagnostic can say what to write.
type createTimeAttr struct{ name, current, requested string }

// refusedCreateTimeChanges lists the create-time attributes a plan changes.
//
// The API's update payload carries secretValue, description and tags and
// nothing else. gin is not configured with DisallowUnknownFields, so a request
// naming contentType, maxVersions or recoveryWindowDays is accepted with a 200
// and those three are DISCARDED — after which Update refreshes from the API and
// writes the OLD value into state, so the apply ends in Terraform's "Provider
// produced inconsistent result after apply" and the platform never took the
// change. Refusing is the honest answer while the API cannot change them.
//
// ponytail: an unknown plan value is skipped. It only arises when the attribute
// is wired to another resource's not-yet-known output, where the resolved value
// may well equal state; Update re-checks with both sides known.
func refusedCreateTimeChanges(plan, state SecretModel) []createTimeAttr {
	var refused []createTimeAttr
	if !plan.ContentType.IsUnknown() && !plan.ContentType.Equal(state.ContentType) {
		refused = append(refused, createTimeAttr{
			"content_type",
			fmt.Sprintf("%q", state.ContentType.ValueString()),
			fmt.Sprintf("%q", plan.ContentType.ValueString()),
		})
	}
	if !plan.MaxVersions.IsUnknown() && !plan.MaxVersions.Equal(state.MaxVersions) {
		refused = append(refused, createTimeAttr{
			"max_versions",
			fmt.Sprintf("%d", state.MaxVersions.ValueInt64()),
			fmt.Sprintf("%d", plan.MaxVersions.ValueInt64()),
		})
	}
	if !plan.RecoveryWindowDays.IsUnknown() && !plan.RecoveryWindowDays.Equal(state.RecoveryWindowDays) {
		refused = append(refused, createTimeAttr{
			"recovery_window_days",
			fmt.Sprintf("%d", state.RecoveryWindowDays.ValueInt64()),
			fmt.Sprintf("%d", plan.RecoveryWindowDays.ValueInt64()),
		})
	}
	return refused
}

// createTimeAttrDetail carries the remedy, and it deliberately does NOT say
// "destroy and recreate": a delete is a SOFT delete that holds the name for
// recovery_window_days (UNIQUE(tenant_id, name) has no partial predicate), so a
// same-name recreate is refused with a 409 for up to 30 days — which is also why
// RequiresReplace() would have been the wrong mechanism here.
const createTimeAttrDetail = "The Frostmoln API can change only the value, description and tags of an " +
	"existing secret; %[1]s is fixed when the secret is created. This secret has %[1]s = %[2]s and the " +
	"configuration requests %[3]s, which the API would discard.\n\n" +
	"Set %[1]s = %[2]s to match the secret, or drop it from the configuration — an omitted value keeps " +
	"what the platform holds, which after an import is the platform's value rather than this resource's " +
	"schema default.\n\n" +
	"To hold a secret with a different %[1]s, change `name` too: that replaces the resource under a new " +
	"name. Destroying and recreating under the SAME name does not work — deleting a secret starts a " +
	"recovery window of recovery_window_days, during which the name stays taken."

func addCreateTimeRefusals(diags *diag.Diagnostics, refused []createTimeAttr) {
	for _, a := range refused {
		diags.AddAttributeError(
			path.Root(a.name),
			a.name+" cannot be changed after creation",
			fmt.Sprintf(createTimeAttrDetail, a.name, a.current, a.requested),
		)
	}
}

func (r *secretResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // Create or destroy: nothing to compare.
	}

	var plan, state SecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A name change already replaces the resource and the new values land at
	// create, so refusing here would block the one remedy that works. The
	// framework calls ModifyPlan with the prior state present before it decides
	// on replacement, so this cannot be read off resp.RequiresReplace.
	if !plan.Name.Equal(state.Name) {
		return
	}

	addCreateTimeRefusals(&resp.Diagnostics, refusedCreateTimeChanges(plan, state))
}

// secretValueWO is the write-only triple for the secret value.
//
// This is the resource with the most to lose from the plan-time validators
// skipping an unknown value: an empty value here is not dropped, it is SENT.
// Against secrets v0.12.23 that is a failed apply; against any server without
// that guard it is written as a new secret version, with no way back.
// ExactlyOne, because secret_value was Required before the write-only form
// existed. See the package comment on internal/writeonly.
var secretValueWO = writeonly.Attr{ // pragma: allowlist secret
	WO:         "secret_value_wo",
	Version:    "secret_value_wo_version",
	Legacy:     "secret_value",
	Subject:    "the secret",
	ExactlyOne: true,
}
