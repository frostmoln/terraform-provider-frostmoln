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

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/unenacted"
)

var _ resource.Resource = &webserverDomainResource{}

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
}

func (r *webserverDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webserver_domain"
}

func (r *webserverDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a domain binding for a webserver instance in the Frostmoln platform. This resource is create/delete only — domains cannot be updated in place." + "\n\n" + scopedecl.Summary("frostmoln_webserver_domain"),
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

	apiResp, err := r.client.Post(ctx, r.domainsPath(plan.InstanceID.ValueString()), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create webserver domain", err.Error())
		return
	}

	var domain apiWebserverDomain
	if err := json.Unmarshal(apiResp.Body, &domain); err != nil {
		resp.Diagnostics.AddError("Failed to parse webserver domain response", err.Error())
		return
	}

	// The Add endpoint is synchronous and returns the created binding directly
	// (there is no status field to poll on, and no get-single endpoint).
	plan.ID = types.StringValue(domain.ID)
	plan.InstanceID = types.StringValue(domain.InstanceID)
	plan.DomainName = types.StringValue(domain.DomainName)
	plan.TLSEnabled = types.BoolValue(domain.TLSEnabled)
	plan.IsDefault = types.BoolValue(domain.IsDefault)
	plan.CreatedAt = types.StringValue(domain.CreatedAt)

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
		if list.Domains[i].ID == domainID {
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

	state.ID = types.StringValue(domain.ID)
	state.InstanceID = types.StringValue(domain.InstanceID)
	state.DomainName = types.StringValue(domain.DomainName)
	state.TLSEnabled = types.BoolValue(domain.TLSEnabled)
	state.IsDefault = types.BoolValue(domain.IsDefault)
	state.CreatedAt = types.StringValue(domain.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *webserverDomainResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// This resource is create/delete only. All mutable attributes have RequiresReplace(),
	// so Terraform will never call Update — it will destroy and recreate instead.
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Webserver domain bindings cannot be updated in place. Terraform should destroy and recreate the resource.",
	)
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
