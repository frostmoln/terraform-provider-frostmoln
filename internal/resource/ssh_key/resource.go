package ssh_key

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                 = &sshKeyResource{}
	_ resource.ResourceWithImportState  = &sshKeyResource{}
	_ resource.ResourceWithUpgradeState = &sshKeyResource{}
	_ resource.ResourceWithModifyPlan   = &sshKeyResource{}
)

// NewResource returns a new SSH key resource factory.
func NewResource() resource.Resource {
	return &sshKeyResource{}
}

type sshKeyResource struct {
	client *client.Client
}

func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// v1: the resource ID moved from the backend uuid to the key name. See
		// UpgradeState for the v0→v1 migration.
		Version:     1,
		Description: "Manages an SSH key in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_ssh_key"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The identifier of the SSH key. Compute identifies keys by name " +
					"within a tenant, so this equals the key name. Import by name: " +
					"`terraform import frostmoln_ssh_key.<label> <key-name>`.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the SSH key.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_key": schema.StringAttribute{
				Description: "The public key content.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"fingerprint": schema.StringAttribute{
				Description: "The fingerprint of the SSH key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the SSH key was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": tftags.OfferTagsAttribute("Resource tags on the SSH key: your own key/value metadata for search " +
				"and filtering (`?tag=key=value` in the API, `--tag` in the fm CLI), stored by the platform only — never " +
				"written into an instance. Changing them updates the key in place; the key itself never changes."),
			"tags_all": tftags.TagsAllAttribute(),
		},
	}
}

func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SSHKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest()
	defaults := r.client.DefaultTags()
	apiReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/sshkeys"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create SSH key", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	key, err := client.ParseResponse[apiSSHKey](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse SSH key response", err.Error())
		return
	}

	plan.fromAPI(key)
	plan.tagsFromAPI(ctx, key, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SSHKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/sshkeys/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read SSH key", err.Error())
		return
	}

	key, err := client.ParseResponse[apiSSHKey](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse SSH key response", err.Error())
		return
	}

	state.fromAPI(key)
	state.tagsFromAPI(ctx, key, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// get reads one key by name.
func (r *sshKeyResource) get(ctx context.Context, name string) (*apiSSHKey, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/sshkeys/"+url.PathEscape(name)), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiSSHKey](apiResp)
}

// Update changes the key's tags — its only in-place attribute (every other one
// replaces the key) — with a tags-only PATCH, sent only when the platform's set
// would change (computed against the tags the key holds right now, so a key
// added outside Terraform is kept).
func (r *sshKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SSHKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := r.get(ctx, name)
			if err != nil {
				return nil, err
			}
			return tftags.OfferCustomerTags(cur.Tags), nil
		}, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if tags != nil {
		if _, err := r.client.Patch(ctx, r.client.TenantPath("/sshkeys/"+url.PathEscape(name)), apiUpdateTagsRequest{Tags: tags}); err != nil {
			resp.Diagnostics.AddError("Failed to Update SSH Key Tags", err.Error())
			return
		}
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it; that also ends an import's pending state.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	key, err := r.get(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read SSH Key After Update", err.Error())
		return
	}
	plan.fromAPI(key)
	plan.tagsFromAPI(ctx, key, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the key's tags.
func (r *sshKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}

func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SSHKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/sshkeys/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete SSH key", err.Error())
	}
}

func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID is the key name (compute's per-tenant identifier); Read then
	// populates the rest from GET /sshkeys/{name}.
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

// UpgradeState migrates v0 state (resource ID = backend uuid) to v1 (ID = key
// name). v0 Created keys with id=uuid; the key name is already present in v0
// state, so the migration is purely local (no API call). Without it, the first
// post-upgrade refresh would GET /sshkeys/{uuid} → 404 → recreate → POST →
// 409 conflict (the name still exists), failing the apply.
func (r *sshKeyResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema: &schema.Schema{
				Attributes: map[string]schema.Attribute{
					"id":          schema.StringAttribute{Computed: true},
					"name":        schema.StringAttribute{Required: true},
					"public_key":  schema.StringAttribute{Required: true},
					"fingerprint": schema.StringAttribute{Computed: true},
					"created_at":  schema.StringAttribute{Computed: true},
				},
			},
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var prior sshKeyModelV0
				resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
				if resp.Diagnostics.HasError() {
					return
				}
				// The ID is now the key name, not the backend uuid. tags and
				// tags_all start null; the next refresh reads them.
				resp.Diagnostics.Append(resp.State.Set(ctx, &SSHKeyModel{
					ID: prior.Name, Name: prior.Name, PublicKey: prior.PublicKey,
					Fingerprint: prior.Fingerprint, CreatedAt: prior.CreatedAt,
					Tags: types.MapNull(types.StringType), TagsAll: types.MapNull(types.StringType),
				})...)
			},
		},
	}
}
