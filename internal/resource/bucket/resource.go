package bucket

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &bucketResource{}
	_ resource.ResourceWithImportState = &bucketResource{}
)

// NewResource returns a new bucket resource factory.
func NewResource() resource.Resource {
	return &bucketResource{}
}

type bucketResource struct {
	client *client.Client
}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an object storage bucket in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "The name of the bucket. Also serves as the unique identifier.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"region": schema.StringAttribute{
				Description: "The region where the bucket is located.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"storage_class": schema.StringAttribute{
				Description: "The storage class for the bucket.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"versioning": schema.StringAttribute{
				Description: "The versioning state of the bucket (e.g. enabled, suspended).",
				Optional:    true,
				Computed:    true,
			},
			"tags": schema.MapAttribute{
				Description: "Tags associated with the bucket.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"object_count": schema.Int64Attribute{
				Description: "The number of objects in the bucket.",
				Computed:    true,
			},
			"size_bytes": schema.Int64Attribute{
				Description: "The total size of all objects in the bucket, in bytes.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The S3-compatible endpoint URL for the bucket.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"access_key": schema.StringAttribute{
				Description: "The access key for the bucket.",
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the bucket was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *bucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := plan.toCreateRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/buckets"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create bucket", err.Error())
		return
	}

	b, err := client.ParseResponse[apiBucket](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse bucket response", err.Error())
		return
	}

	resp.Diagnostics.Append(plan.fromAPI(ctx, b)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/buckets/"+state.Name.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read bucket", err.Error())
		return
	}

	b, err := client.ParseResponse[apiBucket](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse bucket response", err.Error())
		return
	}

	// Preserve access_key from state since the API may not return it on read.
	accessKey := state.AccessKey

	resp.Diagnostics.Append(state.fromAPI(ctx, b)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Restore access_key if the API did not include it in the response.
	if b.AccessKey == "" && !accessKey.IsNull() {
		state.AccessKey = accessKey
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := plan.toUpdateRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Patch(ctx, r.client.TenantPath("/buckets/"+plan.Name.ValueString()), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update bucket", err.Error())
		return
	}

	b, err := client.ParseResponse[apiBucket](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse bucket response", err.Error())
		return
	}

	// Preserve access_key from state.
	var state BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	accessKey := state.AccessKey

	resp.Diagnostics.Append(plan.fromAPI(ctx, b)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if b.AccessKey == "" && !accessKey.IsNull() {
		plan.AccessKey = accessKey
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/buckets/"+state.Name.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete bucket", err.Error())
	}
}

func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}
