package bucket

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &bucketResource{}
	_ resource.ResourceWithImportState = &bucketResource{}
	_ resource.ResourceWithModifyPlan  = &bucketResource{}
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
		Description: "Manages an object storage bucket in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_bucket"),
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
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"storage_class": schema.StringAttribute{
				Description: "The default storage class for objects in the bucket. One of `STANDARD`, " +
					"`STANDARD_IA`, `REDUCED_REDUNDANCY`, `GLACIER` — the values are case-sensitive. " +
					"Defaults server-side to `STANDARD`.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"versioning": schema.StringAttribute{
				Description: "The versioning state of the bucket: `enabled` or `suspended`. A bucket starts " +
					"`disabled`, and S3 versioning cannot be turned back off once enabled — only suspended — " +
					"so `disabled` is not settable: the server would answer with the unchanged state and the " +
					"apply would fail on the mismatch.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf("enabled", "suspended"),
				},
			},
			"acl": schema.StringAttribute{
				Description: "Anonymous access to the bucket's objects: `private` or `public-read`. " +
					"`private` (the platform default for a new bucket): objects are reachable only with " +
					"S3 credentials or a presigned URL. `public-read`: anyone on the internet can download " +
					"ANY object in the bucket by its URL, without credentials — every object already in " +
					"the bucket and every object uploaded later — while listing the bucket's contents " +
					"still requires credentials. This is NOT the AWS S3 canned ACL of the same name, " +
					"which grants anonymous listing of the bucket and leaves object downloads to " +
					"per-object ACLs. Changed in place, without replacing the bucket. When omitted, " +
					"Terraform does not manage it: a new bucket is created `private`, and an existing " +
					"bucket keeps whatever access it has.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf("private", "public-read"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Tags associated with the bucket." + tftags.TagsNote,
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"tags_all": tftags.TagsAllAttribute(),
			"object_count": schema.Int64Attribute{
				Description: "The number of objects in the bucket.",
				Computed:    true,
			},
			"size_bytes": schema.Int64Attribute{
				Description: "The total size of all objects in the bucket, in bytes.",
				Computed:    true,
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

	defaults := r.client.DefaultTags()
	apiReq := plan.toCreateRequest()
	apiReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/buckets"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create bucket", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

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

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/buckets/"+url.PathEscape(state.Name.ValueString())), nil)
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

	if b.ACL == nil {
		resp.Diagnostics.Append(aclUnverifiedWarning(state.ACL))
	}
	if b.Versioning == nil {
		resp.Diagnostics.Append(unverifiedWarning("versioning", versioningUnverifiedSummary,
			"versioning state", state.Versioning))
	}
	resp.Diagnostics.Append(state.fromAPI(ctx, b)...)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var config BucketModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	defaults := r.client.DefaultTags()
	apiReq := plan.toUpdateRequest(&config, &state)
	current, err := r.currentTags(ctx, r.client.TenantPath("/buckets/"+url.PathEscape(state.Name.ValueString())))
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	prior := tftags.PriorOf(ctx, state.Tags, state.TagsAll, req.Private, &resp.Diagnostics).WithCurrent(current)
	apiReq.Tags, _ = tftags.ForUpdate(ctx, defaults, plan.Tags, prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// PUT, not PATCH: storage registers only PUT on /buckets/{name}
	// (storage/internal/handler/http/router.go) and does not enable gin's
	// method-not-allowed handling, so a PATCH here answered 404 and every
	// bucket update failed.
	apiResp, err := r.client.Put(ctx, r.client.TenantPath("/buckets/"+url.PathEscape(plan.Name.ValueString())), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update bucket", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

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

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/buckets/"+url.PathEscape(state.Name.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete bucket", err.Error())
	}
}

func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

// ModifyPlan predicts tags_all: unknown whenever the next write changes the
// platform's tag set, including a change to the provider's default_tags.
//
// It also warns when a public bucket's configuration sets no acl. Omitting
// acl means "not managed", so removing `acl = "public-read"` plans nothing
// and the bucket stays public — a silent empty plan is exactly the wrong
// signal for that, so the plan says it.
func (r *bucketResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)

	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // create or destroy: nothing to warn about
	}

	var configACL, stateACL types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("acl"), &configACL)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("acl"), &stateACL)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if configACL.IsNull() && stateACL.ValueString() == aclPublicRead {
		resp.Diagnostics.AddAttributeWarning(path.Root("acl"), aclStaysPublicSummary, aclStaysPublicDetail)
	}
}

const (
	aclPublicRead = "public-read"

	aclStaysPublicSummary = "Bucket Stays Public"
	aclStaysPublicDetail  = "This bucket is public-read: anyone can download its objects by URL, " +
		"without credentials. The configuration sets no acl, so Terraform leaves the bucket's access " +
		"as it is and the bucket stays public. To make it private, set acl = \"private\"."

	aclUnverifiedSummary        = "Bucket Access Could Not Be Verified"
	versioningUnverifiedSummary = "Bucket Versioning Could Not Be Verified"
)

// aclUnverifiedWarning is the refresh-time warning for a read that came back
// without the bucket's access setting.
func aclUnverifiedWarning(prior types.String) diag.Diagnostic {
	return unverifiedWarning("acl", aclUnverifiedSummary, "access setting", prior)
}

// unverifiedWarning is the refresh-time warning for a read that came back
// without one of the bucket's settings: state keeps the last known value, and
// the practitioner must be told that value is not a fresh answer.
func unverifiedWarning(attr, summary, what string, prior types.String) diag.Diagnostic {
	last := "no value"
	if !prior.IsNull() && !prior.IsUnknown() {
		last = fmt.Sprintf("%q", prior.ValueString())
	}
	return diag.NewAttributeWarningDiagnostic(path.Root(attr), summary,
		fmt.Sprintf("The platform could not read this bucket's %s during this refresh, "+
			"so %s shows the last known value (%s), which may no longer be current. Refresh again "+
			"to verify it.", what, attr, last))
}

// currentTags reads the tags the platform holds right now, at memberPath. An update
// derives the keys it keeps — the ones this configuration does not manage —
// from this rather than from state, which is only as fresh as the last
// refresh (tftags.Prior.WithCurrent).
func (r *bucketResource) currentTags(ctx context.Context, memberPath string) (map[string]string, error) {
	apiResp, err := r.client.Get(ctx, memberPath, nil)
	if err != nil {
		return nil, err
	}
	obj, err := client.ParseResponse[apiBucket](apiResp)
	if err != nil {
		return nil, err
	}
	return obj.customerTags(), nil
}
