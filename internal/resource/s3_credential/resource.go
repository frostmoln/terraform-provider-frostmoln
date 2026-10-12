package s3_credential

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &s3CredentialResource{}
	_ resource.ResourceWithImportState = &s3CredentialResource{}
	_ resource.ResourceWithModifyPlan  = &s3CredentialResource{}
)

// missingIDSummary is raised when a credential in state has no access key ID.
// Provider versions up to v0.17.1 decoded the identifier from an `id` field the
// API does not send, so state written by them carries an empty id. Guessing the
// credential (e.g. by name — names are not unique) could bind Terraform to the
// wrong live key, so both Read and Delete stop and ask for a re-import instead.
const missingIDSummary = "S3 credential is missing its access key ID"

const missingIDRemedy = "State written by provider versions up to v0.17.1 carries an empty id, because the " +
	"identifier was decoded from a field the API does not send. The credential itself still exists and its " +
	"keys still work — Terraform just cannot address it.\n\n" +
	"Find its access key ID in the portal or with `fm storage s3-credential list`, then either re-import it:\n" +
	"  terraform state rm <address> && terraform import <address> <accessKeyId>\n" +
	"(secret_access_key cannot be recovered on import — the API returns it only at creation), or delete the " +
	"credential and remove it from state with `terraform state rm <address>`."

// NewResource returns a new S3 credential resource factory.
func NewResource() resource.Resource {
	return &s3CredentialResource{}
}

type s3CredentialResource struct {
	client *client.Client
}

func (r *s3CredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_credential"
}

func (r *s3CredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an S3 credential in the Frostmoln platform. Credentials are immutable apart " +
			"from their tags: changing the name, description, or any scope attribute (allowed_buckets/" +
			"allowed_actions/ip_whitelist) replaces the credential and issues a new secret_access_key, so " +
			"update any downstream consumers; changing `tags` updates it in place. Per-credential scoping requires an IAM-capable object-storage backend." +
			"\n\n" + scopedecl.Summary("frostmoln_s3_credential"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The access key ID of the S3 credential — the identifier used with secret_access_key when talking to the S3 endpoint, and the value to pass to terraform import.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the S3 credential.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A description of the S3 credential.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"allowed_buckets": schema.ListAttribute{
				Description: "Buckets this credential may access. Empty/unset = ALL buckets in the tenant's account — set it to apply least privilege. Changing this replaces the credential.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"allowed_actions": schema.ListAttribute{
				Description: "S3 actions this credential may perform, e.g. s3:GetObject. Empty/unset = ALL actions — set it to apply least privilege. Bucket-metadata writes (s3:PutBucketAcl, s3:PutBucketPolicy, s3:PutBucketTagging) are not grantable and are rejected. Changing this replaces the credential.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"ip_whitelist": schema.ListAttribute{
				Description: "Source IPs/CIDRs to restrict this credential to. TEMPORARILY UNAVAILABLE: must be empty or unset " +
					"(= any source IP). The platform refuses a non-empty list, and so does `terraform plan`, before the " +
					"credential is replaced. Changing this replaces the credential.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					ipWhitelistMustBeEmpty{},
				},
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"secret_access_key": schema.StringAttribute{
				Description: "The secret access key. Only returned when the credential is first created. " +
					docs.StateSecretNote,
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the S3 credential.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the S3 credential was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": tftags.OfferTagsAttribute("Resource tags on the credential: your own key/value metadata for " +
				"search and filtering (`?tag=key=value` in the API, `--tag` in the fm CLI). Labels only — they never " +
				"reach the S3 endpoint or the credential's access, and changing them updates the credential in place " +
				"(no new secret). The provider's `default_tags` apply as usual; the tenant's default tags are NOT " +
				"added by the platform when a credential is created (they reach credentials only through " +
				"\"apply default tags to existing resources\")."),
			"tags_all": tftags.TagsAllAttribute(),
		},
	}
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the credential's tags.
func (r *s3CredentialResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}

func (r *s3CredentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *s3CredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan S3CredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := plan.toCreateRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	defaults := r.client.DefaultTags()
	apiReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/credentials"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create S3 credential", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	cred, err := client.ParseResponse[apiS3Credential](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse S3 credential response", err.Error())
		return
	}

	resp.Diagnostics.Append(plan.fromAPI(ctx, cred)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.tagsFromAPI(ctx, cred, &resp.Diagnostics)
	// Persist state before failing on a missing id: the credential was created
	// and its secret_access_key is returned only once, so dropping the state
	// would lose it. The error tells the operator how to reconcile.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if cred.ID == "" {
		resp.Diagnostics.AddError(
			missingIDSummary,
			"The API created the credential but returned no accessKeyId, so Terraform cannot address it. "+
				"Its secret_access_key is in state and cannot be fetched again, so prefer editing the id in "+
				"place (terraform state pull / push) over a re-import.\n\n"+missingIDRemedy,
		)
	}
}

func (r *s3CredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state S3CredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddError(missingIDSummary, missingIDRemedy)
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/credentials/"+url.PathEscape(state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read S3 credential", err.Error())
		return
	}

	found, err := client.ParseResponse[apiS3Credential](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse S3 credential response", err.Error())
		return
	}

	// Never let a response rewrite the identity of the resource being read: a
	// body that is not the requested credential (a misrouted path collapsing to
	// the collection, a shape change) would otherwise blank state's id via
	// fromAPI and strand the credential.
	if found.ID != state.ID.ValueString() {
		resp.Diagnostics.AddError(
			"Unexpected S3 credential in read response",
			fmt.Sprintf("Requested %q but the API answered with %q.", state.ID.ValueString(), found.ID),
		)
		return
	}

	// Preserve secret_access_key from state since the API does not return it on read.
	secretKey := state.SecretAccessKey // pragma: allowlist secret

	resp.Diagnostics.Append(state.fromAPI(ctx, found)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Restore the secret key from state since the API only returns it on create.
	if found.SecretAccessKey == "" && !secretKey.IsNull() && !secretKey.IsUnknown() { // pragma: allowlist secret
		state.SecretAccessKey = secretKey // pragma: allowlist secret
	}
	state.tagsFromAPI(ctx, found, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// get reads one credential.
func (r *s3CredentialResource) get(ctx context.Context, id string) (*apiS3Credential, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/credentials/"+url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiS3Credential](apiResp)
}

// Update changes the credential's tags — its only in-place attribute (every
// other one replaces it) — with a PUT carrying `tags` alone, sent only when the
// platform's set would change. It writes nothing at the S3 endpoint and never
// issues a new secret.
func (r *s3CredentialResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state S3CredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError(missingIDSummary, missingIDRemedy)
		return
	}
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := r.get(ctx, id)
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
		if _, err := r.client.Put(ctx, r.client.TenantPath("/credentials/"+url.PathEscape(id)), apiUpdateTagsRequest{Tags: tags}); err != nil {
			resp.Diagnostics.AddError("Failed to Update S3 Credential Tags", err.Error())
			return
		}
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	cred, err := r.get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read S3 Credential After Update", err.Error())
		return
	}
	secretKey := state.SecretAccessKey // pragma: allowlist secret
	resp.Diagnostics.Append(plan.fromAPI(ctx, cred)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.SecretAccessKey = secretKey // pragma: allowlist secret
	plan.tagsFromAPI(ctx, cred, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *s3CredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state S3CredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Without this guard the request would be DELETE on the collection path,
	// which answers 404 and would be swallowed below as "already gone" —
	// reporting a successful destroy while the credential stays live.
	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddError(missingIDSummary, missingIDRemedy)
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/credentials/"+url.PathEscape(state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete S3 credential", err.Error())
	}
}

func (r *s3CredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Dot-segments survive url.PathEscape (unreserved characters) and are then
	// collapsed by the client's path.Join, so an import ID like "." would aim
	// requests at the credentials collection instead of one credential. Reject
	// them here, the trust boundary where an operator-supplied ID enters state
	// (same guard as kubernetes_node_pool).
	if req.ID == "" || req.ID == "." || req.ID == ".." || strings.Contains(req.ID, "/") {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("%q is not a valid S3 credential access key ID.", req.ID),
		)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}
