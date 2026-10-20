// Package webserver_domain implements the frostmoln_webserver_domain Terraform resource.
package webserver_domain

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/unenacted"
)

var (
	_ resource.Resource                = &webserverDomainResource{}
	_ resource.ResourceWithModifyPlan  = &webserverDomainResource{}
	_ resource.ResourceWithImportState = &webserverDomainResource{}
)

// NewResource returns a new webserver_domain resource factory.
func NewResource() resource.Resource {
	return &webserverDomainResource{}
}

// CLASS A (the 2026-09 convergence audit, finding A3): a domain binding row
// is written and read back, and nothing consumes it. The webserver service's
// Add validates and inserts; its Remove deletes; the config renderer emits
// index/client_max_body_size/gzip/security headers/error_page/SPA fallback
// and no server_name directive of any kind (webserver never sends it down
// the gRPC create), and no certificate workflow reaches a domain binding —
// so tls_enabled = true records a row that provisions neither TLS nor
// anything else. The row itself stays create/delete-only (honest for as
// long as its consumers exist); the TLS PROMISE is what gets the refusal —
// `true` promises HTTPS the platform does not serve on create or update
// alike (the same gap the portal and fm mirror). With the ADR-0091 config
// pipeline now existing (ApplyTypeConfig → config_version → in-guest
// agent), the honest platform fix is genuinely reachable; until it ships,
// the provider refuses to record the promise.
const tlsEnabledRefusalTitle = "tls_enabled does not provision TLS"

const tlsEnabledRefusalDetail = tlsEnabledRefusalTitle +
	": the webserver platform stores a domain binding row and never consumes it — no vhost is " +
	"rendered from the binding and no certificate path exists, so no tls setting on this resource " +
	"makes the site serve HTTPS over the bound domain on create or on update. The portal and fm " +
	"create the same inert rows; Terraform refuses the promise rather than report TLS that " +
	"does not exist.\n\n" +
	"Omit tls_enabled (or set false, which matches what the platform actually serves). Serve HTTPS " +
	"through an `frostmoln_application_gateway` meanwhile, and wait for the platform's domain/TLS " +
	"enact path."

// webserverDomainResource is synchronous end to end — Create is a direct POST
// that returns the binding, Delete is a synchronous 204 — so it carries no
// wait, no poll knobs and no timeouts block.
type webserverDomainResource struct {
	client *client.Client
}

// webserverDomainModel is the Terraform state model for a webserver domain binding.
type webserverDomainModel struct {
	ID         types.String `tfsdk:"id"`
	InstanceID types.String `tfsdk:"instance_id"`
	DomainName types.String `tfsdk:"domain_name"`
	TLSEnabled types.Bool   `tfsdk:"tls_enabled"`
	IsDefault  types.Bool   `tfsdk:"is_default"`
	CreatedAt  types.String `tfsdk:"created_at"`
	// Tags / TagsAll are the binding's customer resource tags (internal/tftags).
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`
}

// apiWebserverDomain is the API representation of a webserver domain binding.
// The webserver service (webserver/internal/domain/domain_binding.go) does NOT
// return a status field, and there is no get-single endpoint — domains are read
// from the list (GET /webservers/{id}/domains).
type apiWebserverDomain struct {
	ID         string `json:"id"`
	InstanceID string `json:"instanceId"`
	DomainName string `json:"domainName"`
	TLSEnabled bool   `json:"tlsEnabled"`
	IsDefault  bool   `json:"isDefault"`
	CreatedAt  string `json:"createdAt"`
	// Tags are the binding's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
}

// apiWebserverDomainList is the list response for a webserver instance's domains.
type apiWebserverDomainList struct {
	Domains []apiWebserverDomain `json:"domains"`
}

// apiCreateWebserverDomainRequest is the API request to create a webserver domain binding.
type apiCreateWebserverDomainRequest struct {
	DomainName string `json:"domainName"`
	TLSEnabled *bool  `json:"tlsEnabled,omitempty"`
	IsDefault  *bool  `json:"isDefault,omitempty"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags in.
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateTagsRequest is PATCH .../domains/{id}: `tags` is the binding's only
// editable field (any other is refused), a row-only write. A POINTER so an empty
// map is sent as {} (clear).
type apiUpdateTagsRequest struct {
	Tags *map[string]string `json:"tags,omitempty"`
}

// fromAPI copies a binding into the model; m.Tags must hold the prior value (the
// configuration on a create or update, the state on a refresh).
func (m *webserverDomainModel) fromAPI(ctx context.Context, d *apiWebserverDomain, diags *diag.Diagnostics) {
	m.ID = types.StringValue(d.ID)
	m.InstanceID = types.StringValue(d.InstanceID)
	m.DomainName = types.StringValue(d.DomainName)
	m.TLSEnabled = types.BoolValue(d.TLSEnabled)
	m.IsDefault = types.BoolValue(d.IsDefault)
	m.CreatedAt = types.StringValue(d.CreatedAt)
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(d.Tags), m.Tags, diags)
}

func (r *webserverDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webserver_domain"
}

func (r *webserverDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a domain binding for a webserver instance in the Frostmoln platform. Every attribute except `tags` is fixed: changing one replaces the binding." + "\n\n" + scopedecl.Summary("frostmoln_webserver_domain"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the domain binding.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the webserver instance to bind the domain to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"domain_name": schema.StringAttribute{
				Description: "The domain name to bind (e.g. \"example.com\", \"www.example.com\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tls_enabled": schema.BoolAttribute{
				Description: "Whether TLS is enabled for this domain. Setting `true` is refused at plan time: the binding is stored but never consumed — no vhost is rendered from it and no certificate path exists — so no TLS is provisioned on create or update. Serve HTTPS through an `frostmoln_application_gateway` meanwhile.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					boolplanmodifier.RequiresReplace(),
				},
				Validators: []validator.Bool{
					unenacted.BoolTrue(tlsEnabledRefusalTitle, tlsEnabledRefusalDetail),
				},
			},
			"is_default": schema.BoolAttribute{
				Description: "Whether this is the default domain for the webserver instance.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					boolplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the domain binding was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
	resp.Schema.Attributes["tags"] = tftags.OfferTagsAttribute("Resource tags on the domain binding: your own " +
		"key/value metadata for search and filtering, stored by the platform only. Changing them updates the " +
		"binding in place.")
	resp.Schema.Attributes["tags_all"] = tftags.TagsAllAttribute()
}

func (r *webserverDomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *webserverDomainResource) domainsPath(instanceID string) string {
	return r.client.TenantPath("/webservers/" + instanceID + "/domains")
}

func (r *webserverDomainResource) domainPath(instanceID, domainID string) string {
	return r.client.TenantPath("/webservers/" + instanceID + "/domains/" + domainID)
}

func (r *webserverDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan webserverDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Belt for the plan-time unenacted validator (see the constants above): a
	// configuration value that resolves only at apply is still unknown when
	// the plan is validated, and Create is the last honest word before the
	// promise would be POSTed. By apply every value is known.
	if plan.TLSEnabled.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("tls_enabled"), tlsEnabledRefusalTitle, tlsEnabledRefusalDetail,
		)
		return
	}

	apiReq := apiCreateWebserverDomainRequest{
		DomainName: plan.DomainName.ValueString(),
	}
	if !plan.TLSEnabled.IsNull() && !plan.TLSEnabled.IsUnknown() {
		v := plan.TLSEnabled.ValueBool()
		apiReq.TLSEnabled = &v
	}
	if !plan.IsDefault.IsNull() && !plan.IsDefault.IsUnknown() {
		v := plan.IsDefault.ValueBool()
		apiReq.IsDefault = &v
	}
	defaults := r.client.DefaultTags()
	apiReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.domainsPath(plan.InstanceID.ValueString()), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create webserver domain", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	var domain apiWebserverDomain
	if err := json.Unmarshal(apiResp.Body, &domain); err != nil {
		resp.Diagnostics.AddError("Failed to parse webserver domain response", err.Error())
		return
	}

	// The Add endpoint is synchronous and returns the created binding directly
	// (there is no status field to poll on, and no get-single endpoint).
	plan.fromAPI(ctx, &domain, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// findDomain fetches the instance's domain list and returns the binding with the
// given id (there is no get-single endpoint).
func (r *webserverDomainResource) findDomain(ctx context.Context, instanceID, domainID string) (*apiWebserverDomain, error) {
	apiResp, err := r.client.Get(ctx, r.domainsPath(instanceID), nil)
	if err != nil {
		return nil, err
	}
	var list apiWebserverDomainList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		return nil, err
	}
	for i := range list.Domains {
		if pubid.Equal(list.Domains[i].ID, domainID) {
			return &list.Domains[i], nil
		}
	}
	return nil, nil
}

func (r *webserverDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webserverDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domain, err := r.findDomain(ctx, state.InstanceID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read webserver domain", err.Error())
		return
	}
	if domain == nil {
		// The binding is no longer present in the instance's domain list.
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAPI(ctx, domain, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the binding's tags — its only in-place attribute — with a
// tags-only PATCH, sent only when the platform's set would change (computed
// against the tags the binding holds right now, so a key added outside Terraform
// is kept).
func (r *webserverDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state webserverDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	instanceID, id := state.InstanceID.ValueString(), state.ID.ValueString()
	find := func(ctx context.Context) (*apiWebserverDomain, error) {
		d, err := r.findDomain(ctx, instanceID, id)
		if err == nil && d == nil {
			err = fmt.Errorf("domain binding %s is no longer bound to instance %s", id, instanceID)
		}
		return d, err
	}
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := find(ctx)
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
		if _, err := r.client.Patch(ctx, r.domainPath(instanceID, id), apiUpdateTagsRequest{Tags: tags}); err != nil {
			resp.Diagnostics.AddError("Failed to update webserver domain tags", err.Error())
			return
		}
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it; that also ends an import's pending state.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	domain, err := find(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read webserver domain after update", err.Error())
		return
	}
	plan.fromAPI(ctx, domain, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the binding's tags.
func (r *webserverDomainResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}

// ImportState takes "<instance_id>/<domain_id>": the binding is read from its
// instance's domain list, so both are needed.
func (r *webserverDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "instance_id", "domain_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

func (r *webserverDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webserverDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := state.InstanceID.ValueString()
	id := state.ID.ValueString()

	// Remove is synchronous (HTTP 204), so no polling is required.
	_, err := r.client.Delete(ctx, r.domainPath(instanceID, id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete webserver domain", err.Error())
		return
	}
}
