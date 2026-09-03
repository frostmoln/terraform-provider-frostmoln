package secret

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/writeonly"
)

var (
	_ resource.Resource                     = &secretResource{}
	_ resource.ResourceWithImportState      = &secretResource{}
	_ resource.ResourceWithConfigValidators = &secretResource{}
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
					"and the value must be at least one character: the API writes an empty value as a new " +
					"secret version, and there is no way back from that. " +
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
					// An empty value is not an update, it is a destroy: the API
					// writes it as a new version and there is no way back.
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
				Description: "The content type of the secret value. Defaults to \"text/plain\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("text/plain"),
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
				Description: "The maximum number of versions to retain. Defaults to 10.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(10),
			},
			"recovery_window_days": schema.Int64Attribute{
				Description: "The number of days to retain a deleted secret before permanent removal. Defaults to 7.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(7),
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

// secretValueWO is the write-only triple for the secret value.
//
// This is the resource with the most to lose from the plan-time validators
// skipping an unknown value: an empty value here is not dropped, it is WRITTEN,
// as a new secret version, and the schema description says of that "there is no
// way back from that". ExactlyOne, because secret_value was Required before the
// write-only form existed. See the package comment on internal/writeonly.
var secretValueWO = writeonly.Attr{ // pragma: allowlist secret
	WO:         "secret_value_wo",
	Version:    "secret_value_wo_version",
	Legacy:     "secret_value",
	Subject:    "the secret",
	ExactlyOne: true,
}
