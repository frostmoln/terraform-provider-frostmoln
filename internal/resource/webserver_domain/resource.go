// Package webserver_domain implements the frostmoln_webserver_domain Terraform resource.
package webserver_domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var _ resource.Resource = &webserverDomainResource{}

// NewResource returns a new webserver_domain resource factory.
func NewResource() resource.Resource {
	return &webserverDomainResource{}
}

type webserverDomainResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *webserverDomainResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *webserverDomainResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 10 * time.Minute
}

// webserverDomainModel is the Terraform state model for a webserver domain binding.
type webserverDomainModel struct {
	ID         types.String `tfsdk:"id"`
	InstanceID types.String `tfsdk:"instance_id"`
	DomainName types.String `tfsdk:"domain_name"`
	TLSEnabled types.Bool   `tfsdk:"tls_enabled"`
	IsDefault  types.Bool   `tfsdk:"is_default"`
	Status     types.String `tfsdk:"status"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

// apiWebserverDomain is the API representation of a webserver domain binding.
type apiWebserverDomain struct {
	ID         string `json:"id"`
	InstanceID string `json:"instanceId"`
	DomainName string `json:"domainName"`
	TLSEnabled bool   `json:"tlsEnabled"`
	IsDefault  bool   `json:"isDefault"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
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
		Description: "Manages a domain binding for a webserver instance in the Frostmoln platform. This resource is create/delete only — domains cannot be updated in place.",
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
				Description: "Whether TLS is enabled for this domain.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Description: "Whether this is the default domain for the webserver instance.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the domain binding.",
				Computed:    true,
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

	plan.ID = types.StringValue(domain.ID)
	plan.InstanceID = types.StringValue(domain.InstanceID)
	plan.DomainName = types.StringValue(domain.DomainName)
	plan.TLSEnabled = types.BoolValue(domain.TLSEnabled)
	plan.IsDefault = types.BoolValue(domain.IsDefault)
	plan.Status = types.StringValue(domain.Status)
	plan.CreatedAt = types.StringValue(domain.CreatedAt)

	// Save state immediately so the ID is tracked, even if polling fails.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString()

	// Poll until domain reaches "active" status.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"active"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "webserver_domain",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.domainPath(instanceID, domain.ID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			var current apiWebserverDomain
			if parseErr := json.Unmarshal(pollResp.Body, &current); parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Webserver domain failed to reach active state", err.Error())
		return
	}

	// Refresh state after polling.
	readResp, err := r.client.Get(ctx, r.domainPath(instanceID, domain.ID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read webserver domain after creation", err.Error())
		return
	}
	var finalDomain apiWebserverDomain
	if err := json.Unmarshal(readResp.Body, &finalDomain); err != nil {
		resp.Diagnostics.AddError("Failed to parse webserver domain response", err.Error())
		return
	}

	plan.Status = types.StringValue(finalDomain.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webserverDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webserverDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.domainPath(state.InstanceID.ValueString(), state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read webserver domain", err.Error())
		return
	}

	var domain apiWebserverDomain
	if err := json.Unmarshal(apiResp.Body, &domain); err != nil {
		resp.Diagnostics.AddError("Failed to parse webserver domain response", err.Error())
		return
	}

	state.ID = types.StringValue(domain.ID)
	state.InstanceID = types.StringValue(domain.InstanceID)
	state.DomainName = types.StringValue(domain.DomainName)
	state.TLSEnabled = types.BoolValue(domain.TLSEnabled)
	state.IsDefault = types.BoolValue(domain.IsDefault)
	state.Status = types.StringValue(domain.Status)
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

	_, err := r.client.Delete(ctx, r.domainPath(instanceID, id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete webserver domain", err.Error())
		return
	}

	// Wait for the domain to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "webserver_domain",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.domainPath(instanceID, id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			var current apiWebserverDomain
			if parseErr := json.Unmarshal(pollResp.Body, &current); parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Webserver domain failed to delete", err.Error())
	}
}
