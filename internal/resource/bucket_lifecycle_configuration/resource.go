package bucket_lifecycle_configuration

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// maxLifecycleRules mirrors the service-side cap (storage BucketServiceConfig).
const maxLifecycleRules = 1000

var (
	_ resource.Resource                = &bucketLifecycleConfigurationResource{}
	_ resource.ResourceWithImportState = &bucketLifecycleConfigurationResource{}
)

// NewResource returns a new bucket lifecycle configuration resource factory.
func NewResource() resource.Resource {
	return &bucketLifecycleConfigurationResource{}
}

type bucketLifecycleConfigurationResource struct {
	client *client.Client
}

func (r *bucketLifecycleConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_lifecycle_configuration"
}

func (r *bucketLifecycleConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the lifecycle configuration of an object storage bucket: rules that expire " +
			"objects, expire noncurrent versions, and abort incomplete multipart uploads on a schedule.\n\n" +
			"There is no storage-class transition rule and no per-object-tag filter: objects are not moved " +
			"between storage classes on a schedule, and the lifecycle API does not model tag filters. " +
			"Neither is offered here, so scope a rule with `prefix`.\n\n" +
			"REPLACE SEMANTICS: a bucket carries a single lifecycle configuration and applying this resource " +
			"replaces it whole.\n\n" +
			"DO NOT MANAGE THE SAME BUCKET BOTH HERE AND OVER S3. A rule created with your own S3 " +
			"credentials using a feature this API does not model — a tag filter, a date-based expiry, an " +
			"object-size filter — is read back WITHOUT that feature, and Terraform then rewrites it stripped. " +
			"For a rule that expires objects, losing its filter means the rewritten rule DELETES EVERY " +
			"OBJECT IN THE BUCKET on the schedule that was meant for a subset. The plan shows this as a " +
			"filter disappearing rather than as a deletion scope widening, so read any plan that removes a " +
			"prefix from an expiration rule carefully.",
		Attributes: map[string]schema.Attribute{
			"bucket": schema.StringAttribute{
				Description: "The name of the bucket whose lifecycle configuration this is. Also the import ID.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"rules": schema.ListNestedAttribute{
				Description: "The lifecycle rules, in order. At least one rule is required — an empty list is " +
					"refused rather than sent, because the API treats it as \"delete the whole lifecycle " +
					"configuration\" and answers 204, which would silently clear the bucket's rules from a " +
					"config that reads like it is adding them. Use terraform destroy to leave the bucket " +
					"without a lifecycle configuration.",
				Required: true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.SizeAtMost(maxLifecycleRules),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "Identifier for the rule. Required, and unique within the bucket.",
							Required:    true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"enabled": schema.BoolAttribute{
							Description: "Whether the rule is applied. A disabled rule is kept but does nothing.",
							Required:    true,
						},
						"prefix": schema.StringAttribute{
							Description: "Limits the rule to objects whose key starts with this prefix. " +
								"OMIT IT to apply the rule to every object in the bucket — an empty string is " +
								"refused, because it is not transmitted (the field is dropped when empty) and " +
								"would read back as unset while silently meaning the whole bucket.",
							Optional: true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"expiration_days": schema.Int64Attribute{
							Description: "Delete an object this many days after it was created.",
							Optional:    true,
							Validators: []validator.Int64{
								int64validator.AtLeast(1),
							},
						},
						"noncurrent_version_expiration_days": schema.Int64Attribute{
							Description: "Delete a noncurrent object version this many days after it stopped " +
								"being current. Only meaningful on a bucket with versioning enabled.",
							Optional: true,
							Validators: []validator.Int64{
								int64validator.AtLeast(1),
							},
						},
						"abort_incomplete_multipart_upload_days": schema.Int64Attribute{
							Description: "Abort a multipart upload that has not completed this many days after " +
								"it was started, releasing the parts already stored.",
							Optional: true,
							Validators: []validator.Int64{
								int64validator.AtLeast(1),
							},
						},
					},
				},
			},
		},
	}
}

func (r *bucketLifecycleConfigurationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *bucketLifecycleConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BucketLifecycleConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to set bucket lifecycle configuration", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketLifecycleConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BucketLifecycleConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.path(state.Bucket.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			// Confirm the bucket is really gone before dropping state: a 404
			// here also means "this tenant's object-storage account could not
			// be resolved", and silently forgetting the resource on a backend
			// failure makes the next apply blind-write the stored rules over
			// whatever is actually on the bucket — rules that delete objects.
			exists, checkErr := r.bucketExists(ctx, state.Bucket.ValueString())
			if checkErr == nil && !exists {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError(
				"Failed to read bucket lifecycle configuration",
				fmt.Sprintf("The API reported the lifecycle configuration of bucket %q as not found, but "+
					"the bucket itself is still present, so the configuration was not simply deleted. "+
					"Original error: %s", state.Bucket.ValueString(), err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to read bucket lifecycle configuration", err.Error())
		return
	}

	rules, err := client.ParseResponse[apiLifecycleRules](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse bucket lifecycle response", err.Error())
		return
	}

	// A bucket with no lifecycle configuration answers 200 with an empty rule
	// set rather than 404, so an empty list is how "deleted outside Terraform"
	// arrives. Treat it as gone; leaving it in state would plan an update
	// against a configuration that no longer exists.
	if len(rules.Rules) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAPI(rules.Rules)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketLifecycleConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BucketLifecycleConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to set bucket lifecycle configuration", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketLifecycleConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BucketLifecycleConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.Delete(ctx, r.path(state.Bucket.ValueString())); err != nil {
		if client.IsNotFound(err) {
			// Only "already gone" if the bucket is actually gone. Reporting a
			// successful destroy on any 404 would drop the resource from state
			// while its rules keep expiring the customer's objects on schedule.
			exists, checkErr := r.bucketExists(ctx, state.Bucket.ValueString())
			if checkErr == nil && !exists {
				return
			}
			resp.Diagnostics.AddError(
				"Failed to delete bucket lifecycle configuration",
				fmt.Sprintf("The API reported the lifecycle configuration of bucket %q as not found, but "+
					"the bucket itself is still present, so its rules may still be expiring objects. "+
					"Original error: %s", state.Bucket.ValueString(), err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to delete bucket lifecycle configuration", err.Error())
	}
}

// bucketExists distinguishes "the bucket is gone" from a 404 that means
// something else. The service answers 404 when a tenant's object-storage
// account cannot be resolved, so a backend failure is indistinguishable from an
// absence at the sub-resource — and treating one as the other lets a destroy
// report success while rules that delete objects are still in effect.
func (r *bucketLifecycleConfigurationResource) bucketExists(ctx context.Context, bucket string) (bool, error) {
	_, err := r.client.Get(ctx, r.client.TenantPath("/buckets/"+url.PathEscape(bucket)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *bucketLifecycleConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("bucket"), req, resp)
}

// put writes the whole lifecycle configuration. The API answers 204 with no
// body, so there is nothing to parse back; state is the plan.
func (r *bucketLifecycleConfigurationResource) put(ctx context.Context, m *BucketLifecycleConfigurationModel) error {
	_, err := r.client.Put(ctx, r.path(m.Bucket.ValueString()), m.toAPI())
	return err
}

func (r *bucketLifecycleConfigurationResource) path(bucket string) string {
	return r.client.TenantPath("/buckets/" + url.PathEscape(bucket) + "/lifecycle")
}
