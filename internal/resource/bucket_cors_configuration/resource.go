package bucket_cors_configuration

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// maxCORSRules mirrors the service-side cap (storage BucketServiceConfig).
const maxCORSRules = 100

var (
	_ resource.Resource                = &bucketCORSConfigurationResource{}
	_ resource.ResourceWithImportState = &bucketCORSConfigurationResource{}
	_ resource.ResourceWithModifyPlan  = &bucketCORSConfigurationResource{}
)

// NewResource returns a new bucket CORS configuration resource factory.
func NewResource() resource.Resource {
	return &bucketCORSConfigurationResource{}
}

type bucketCORSConfigurationResource struct {
	client *client.Client
}

func (r *bucketCORSConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_cors_configuration"
}

func (r *bucketCORSConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the CORS configuration of an object storage bucket, so browsers may call the " +
			"bucket's S3 endpoint directly from a web application.\n\n" +
			"REPLACE SEMANTICS, AND ONE CONSEQUENCE WORTH KNOWING: a bucket carries a single CORS " +
			"configuration, and applying this resource replaces it whole. Frostmoln adds a default CORS rule " +
			"for the customer portal's origin to every new bucket, which is what lets the portal's object " +
			"browser upload and list objects browser-direct via presigned URLs. Taking the bucket's CORS " +
			"under Terraform removes that rule unless you declare it yourself, and the portal's object " +
			"browser will stop working for this bucket. The plan warns and names the origins it is about to " +
			"stop allowing; declare the ones you want to keep.\n\n" +
			"That default is applied only when a bucket is created, so nothing restores it later: " +
			"`terraform destroy` on this resource leaves the bucket with NO CORS configuration at all, not " +
			"with the rule it had beforehand. Read the current configuration (terraform import, or the API) " +
			"before you take it over, and re-declare what is actually there — the portal's origin is " +
			"deployment configuration, so do not assume a particular hostname.",
		Attributes: map[string]schema.Attribute{
			"bucket": schema.StringAttribute{
				Description: "The name of the bucket whose CORS configuration this is. Also the import ID.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"rules": schema.ListNestedAttribute{
				Description: "The CORS rules, in order. At least one rule is required — an empty list is " +
					"refused rather than sent, because the API treats it as \"delete the whole CORS " +
					"configuration\" and answers 204, which would wipe the bucket's CORS from a config that " +
					"reads like it is adding rules. Use terraform destroy to leave the bucket without one.",
				Required: true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.SizeAtMost(maxCORSRules),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "An optional identifier for the rule.",
							Optional:    true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"allowed_origins": schema.SetAttribute{
							Description: "Origins allowed to make cross-origin requests, e.g. " +
								"`https://app.example.com`. `*` allows any origin — combined with a write " +
								"method that lets any page on the internet drive a request against this " +
								"bucket, so scope it to the origins you serve unless the bucket is public.",
							Required:    true,
							ElementType: types.StringType,
							Validators: []validator.Set{
								setvalidator.SizeAtLeast(1),
							},
						},
						"allowed_methods": schema.SetAttribute{
							Description: "HTTP methods allowed for the origins. One or more of `GET`, `PUT`, " +
								"`POST`, `DELETE`, `HEAD` — the values are case-sensitive.",
							Required:    true,
							ElementType: types.StringType,
							Validators: []validator.Set{
								setvalidator.SizeAtLeast(1),
								setvalidator.ValueStringsAre(
									stringvalidator.OneOf("GET", "PUT", "POST", "DELETE", "HEAD"),
								),
							},
						},
						"allowed_headers": schema.SetAttribute{
							Description: "Request headers allowed in the preflight response. `*` allows any header.",
							Optional:    true,
							ElementType: types.StringType,
							Validators: []validator.Set{
								setvalidator.SizeAtLeast(1),
							},
						},
						"expose_headers": schema.SetAttribute{
							Description: "Response headers a browser may expose to the calling script, e.g. `ETag`.",
							Optional:    true,
							ElementType: types.StringType,
							Validators: []validator.Set{
								setvalidator.SizeAtLeast(1),
							},
						},
						"max_age_seconds": schema.Int64Attribute{
							Description: "How long a browser may cache the preflight response, in seconds. " +
								"Must be at least 1: the field is omitted from the request when zero, so a 0 " +
								"cannot be transmitted and would read back as unset on every plan.",
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

func (r *bucketCORSConfigurationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *bucketCORSConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BucketCORSConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.put(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketCORSConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BucketCORSConfigurationModel
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
			// whatever is actually on the bucket.
			exists, checkErr := r.bucketExists(ctx, state.Bucket.ValueString())
			if checkErr == nil && !exists {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError(
				"Failed to read bucket CORS configuration",
				fmt.Sprintf("The API reported the CORS configuration of bucket %q as not found, but the "+
					"bucket itself is still present, so the configuration was not simply deleted. "+
					"Original error: %s", state.Bucket.ValueString(), err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to read bucket CORS configuration", err.Error())
		return
	}

	rules, err := client.ParseResponse[apiCORSRules](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse bucket CORS response", err.Error())
		return
	}

	// A bucket with no CORS configuration answers 200 with an empty rule set
	// rather than 404, so an empty list is how "deleted outside Terraform"
	// arrives. Treat it as gone; leaving it in state would plan an update
	// against a configuration that no longer exists.
	if len(rules.Rules) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(state.fromAPI(ctx, rules.Rules)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketCORSConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BucketCORSConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.put(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketCORSConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BucketCORSConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.Delete(ctx, r.path(state.Bucket.ValueString())); err != nil {
		if client.IsNotFound(err) {
			// Only "already gone" if the bucket is actually gone. Reporting a
			// successful destroy on any 404 would drop the resource from state
			// while its CORS configuration is still live on the bucket.
			exists, checkErr := r.bucketExists(ctx, state.Bucket.ValueString())
			if checkErr == nil && !exists {
				return
			}
			resp.Diagnostics.AddError(
				"Failed to delete bucket CORS configuration",
				fmt.Sprintf("The API reported the CORS configuration of bucket %q as not found, but the "+
					"bucket itself is still present, so the configuration may still be in effect. "+
					"Original error: %s", state.Bucket.ValueString(), err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to delete bucket CORS configuration", err.Error())
	}
}

func (r *bucketCORSConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("bucket"), req, resp)
}

// ModifyPlan warns, at plan time, about origins this apply will stop allowing.
//
// A bucket carries one CORS configuration and this resource replaces it whole,
// so the first apply silently removes the default rule the platform adds for
// the customer portal's origin — and the portal's object browser stops working
// for the bucket. A schema description is not a control: nobody reads the
// registry page during a CI apply. Naming the origins in the plan output puts
// the fact next to the change that causes it.
//
// Best-effort by design: a read failure here must never fail a plan, so it is
// swallowed. The warning also cannot hardcode the portal origin — it is
// per-deployment configuration — so it reports whatever is actually configured.
func (r *bucketCORSConfigurationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Nothing to warn about on destroy (Delete's own docs cover it) or when the
	// provider is not configured yet (validate/plan without credentials).
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}

	var plan BucketCORSConfigurationModel
	if diags := req.Plan.Get(ctx, &plan); diags.HasError() {
		return
	}
	if plan.Bucket.IsNull() || plan.Bucket.IsUnknown() {
		return
	}

	// Checked first, and independently of the read below: this one is a
	// property of the planned configuration alone, so it must still fire on a
	// first apply, where there is nothing to compare against.
	if wildcard, methods := wildcardWithWriteMethods(ctx, plan.Rules); wildcard {
		resp.Diagnostics.AddWarning(
			"CORS allows any origin to make write requests",
			fmt.Sprintf(
				"A rule on bucket %q allows origin \"*\" together with the write method(s) %s. Any page on "+
					"the internet can then drive those requests against this bucket from a visitor's browser. "+
					"Scope allowed_origins to the origins you serve unless the bucket is deliberately public.",
				plan.Bucket.ValueString(), strings.Join(methods, ", "),
			),
		)
	}

	current, err := r.fetch(ctx, plan.Bucket.ValueString())
	if err != nil || len(current) == 0 {
		return
	}

	removed := removedOrigins(ctx, current, plan.Rules)
	if len(removed) > 0 {
		resp.Diagnostics.AddWarning(
			"CORS origins will stop being allowed on this bucket",
			fmt.Sprintf(
				"Applying this resource replaces the whole CORS configuration of bucket %q, which currently "+
					"allows origins this configuration does not: %s.\n\n"+
					"Frostmoln adds a CORS rule for the customer portal's origin to every new bucket so the "+
					"portal's object browser can upload and list objects browser-direct. If one of the origins "+
					"above is the portal's, its object browser will stop working for this bucket. Declare that "+
					"origin in a rule to keep it.",
				plan.Bucket.ValueString(), strings.Join(removed, ", "),
			),
		)
	}
}

// removedOrigins returns the origins allowed today that the planned rules no
// longer allow, sorted for a stable message.
func removedOrigins(ctx context.Context, current []apiCORSRule, planned []corsRuleModel) []string {
	plannedOrigins := map[string]bool{}
	for i := range planned {
		for _, o := range stringsFromSetLenient(ctx, planned[i].AllowedOrigins) {
			plannedOrigins[o] = true
		}
	}

	seen := map[string]bool{}
	removed := make([]string, 0)
	for _, rule := range current {
		for _, o := range rule.AllowedOrigins {
			if !plannedOrigins[o] && !seen[o] {
				seen[o] = true
				removed = append(removed, o)
			}
		}
	}
	sort.Strings(removed)
	return removed
}

// wildcardWithWriteMethods reports whether any planned rule pairs a "*" origin
// with a method that changes bucket contents.
func wildcardWithWriteMethods(ctx context.Context, planned []corsRuleModel) (bool, []string) {
	for i := range planned {
		origins := stringsFromSetLenient(ctx, planned[i].AllowedOrigins)
		wildcard := false
		for _, o := range origins {
			if o == "*" {
				wildcard = true
				break
			}
		}
		if !wildcard {
			continue
		}
		writes := make([]string, 0)
		for _, m := range stringsFromSetLenient(ctx, planned[i].AllowedMethods) {
			if m == "PUT" || m == "POST" || m == "DELETE" {
				writes = append(writes, m)
			}
		}
		if len(writes) > 0 {
			sort.Strings(writes)
			return true, writes
		}
	}
	return false, nil
}

// stringsFromSetLenient is the diagnostics-free variant used on the plan path,
// where an unresolvable value simply means "nothing to warn about".
func stringsFromSetLenient(ctx context.Context, set types.Set) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	var out []string
	if diags := set.ElementsAs(ctx, &out, false); diags.HasError() {
		return nil
	}
	return out
}

// fetch reads the bucket's current CORS rules.
func (r *bucketCORSConfigurationResource) fetch(ctx context.Context, bucket string) ([]apiCORSRule, error) {
	apiResp, err := r.client.Get(ctx, r.path(bucket), nil)
	if err != nil {
		return nil, err
	}
	rules, err := client.ParseResponse[apiCORSRules](apiResp)
	if err != nil {
		return nil, err
	}
	return rules.Rules, nil
}

// bucketExists distinguishes "the bucket is gone" from a 404 that means
// something else. The service answers 404 when a tenant's object-storage
// account cannot be resolved, so a backend failure is indistinguishable from an
// absence at the sub-resource — and treating one as the other lets a destroy
// report success while the configuration is still live on the bucket.
func (r *bucketCORSConfigurationResource) bucketExists(ctx context.Context, bucket string) (bool, error) {
	_, err := r.client.Get(ctx, r.client.TenantPath("/buckets/"+url.PathEscape(bucket)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// put writes the whole CORS configuration. The API answers 204 with no body, so
// there is nothing to parse back; state is the plan.
func (r *bucketCORSConfigurationResource) put(ctx context.Context, m *BucketCORSConfigurationModel, diags *diag.Diagnostics) {
	body, d := m.toAPI(ctx)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	if _, err := r.client.Put(ctx, r.path(m.Bucket.ValueString()), body); err != nil {
		diags.AddError("Failed to set bucket CORS configuration", err.Error())
	}
}

func (r *bucketCORSConfigurationResource) path(bucket string) string {
	return r.client.TenantPath("/buckets/" + url.PathEscape(bucket) + "/cors")
}
