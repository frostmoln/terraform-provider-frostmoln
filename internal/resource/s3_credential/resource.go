package s3_credential

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &s3CredentialResource{}
	_ resource.ResourceWithImportState = &s3CredentialResource{}
)

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
		Description: "Manages an S3 credential in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the S3 credential.",
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
			"secret_access_key": schema.StringAttribute{
				Description: "The secret access key. Only returned when the credential is first created.",
				Computed:    true,
				Sensitive:   true,
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
		},
	}
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

	apiReq := plan.toCreateRequest()
	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/credentials"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create S3 credential", err.Error())
		return
	}

	cred, err := client.ParseResponse[apiS3Credential](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse S3 credential response", err.Error())
		return
	}

	plan.fromAPI(cred)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *s3CredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state S3CredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API lists all credentials; we need to find ours by ID.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/credentials"), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read S3 credentials", err.Error())
		return
	}

	list, err := client.ParseResponse[apiS3CredentialList](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse S3 credentials response", err.Error())
		return
	}

	var found *apiS3Credential
	for i := range list.Credentials {
		if list.Credentials[i].ID == state.ID.ValueString() {
			found = &list.Credentials[i]
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	// Preserve secret_access_key from state since the API does not return it on read.
	secretKey := state.SecretAccessKey // pragma: allowlist secret

	state.fromAPI(found)

	// Restore the secret key from state since the API only returns it on create.
	if found.SecretAccessKey == "" && !secretKey.IsNull() && !secretKey.IsUnknown() { // pragma: allowlist secret
		state.SecretAccessKey = secretKey // pragma: allowlist secret
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *s3CredentialResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"S3 credentials cannot be updated. All attribute changes require resource replacement.",
	)
}

func (r *s3CredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state S3CredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/credentials/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete S3 credential", err.Error())
	}
}

func (r *s3CredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
