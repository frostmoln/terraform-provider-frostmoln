package dns_zone

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
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
	_ resource.Resource                = &dnsZoneResource{}
	_ resource.ResourceWithImportState = &dnsZoneResource{}
	_ resource.ResourceWithModifyPlan  = &dnsZoneResource{}
)

type dnsZoneResource struct {
	client *client.Client
}

// NewResource returns a new DNS zone resource.
func NewResource() resource.Resource {
	return &dnsZoneResource{}
}

func (r *dnsZoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (r *dnsZoneResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a managed DNS zone in the Frostmoln Cloud Platform. " +
			"Each primary zone is assigned a delegation nameserver set, exposed as " +
			"the read-only name_servers attribute — delegate your domain at your " +
			"registrar to those name servers." +
			"\n\n" + scopedecl.Summary("frostmoln_dns_zone"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the zone.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The zone name as a fully qualified domain name ending with a dot (e.g. \"example.com.\"), lowercase.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-z0-9._-]+\.$`),
						"must be a lowercase fully qualified domain name ending with a dot, e.g. \"example.com.\"",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"email": schema.StringAttribute{
				Description: "The SOA administrative contact email for the zone.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the zone.",
				Optional:    true,
			},
			"ttl": schema.Int64Attribute{
				Description: "The default record TTL in seconds.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"type": schema.StringAttribute{
				Description: "The zone type (primary).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the zone.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"serial": schema.Int64Attribute{
				Description: "The SOA serial of the zone.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"record_count": schema.Int64Attribute{
				Description: "The number of editable records in the zone.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"name_servers": schema.ListAttribute{
				Description: "The zone's delegation name servers. Delegate your domain at your registrar to exactly these.",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the zone." + tftags.TagsNote,
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"tags_all": tftags.TagsAllAttribute(),
			"created_at": schema.StringAttribute{
				Description: "The creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

func (r *dnsZoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData),
		)
		return
	}

	r.client = c
}

func (r *dnsZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DNSZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// DNS is served synchronously by the network service (Designate-backed,
	// ADR-0073) — create returns the zone directly, no async operation.
	defaults := r.client.DefaultTags()
	createReq := plan.toCreateRequest()
	createReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/dns/zones"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create DNS Zone", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	var zone apiDNSZone
	if err := json.Unmarshal(apiResp.Body, &zone); err != nil {
		resp.Diagnostics.AddError("Failed to Parse DNS Zone Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &zone, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DNSZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read DNS Zone", err.Error())
		return
	}

	var zone apiDNSZone
	if err := json.Unmarshal(apiResp.Body, &zone); err != nil {
		resp.Diagnostics.AddError("Failed to Parse DNS Zone Response", err.Error())
		return
	}

	state.fromAPI(ctx, &zone, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dnsZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DNSZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state DNSZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	defaults := r.client.DefaultTags()
	updateReq := plan.toUpdateRequest()
	current, err := r.currentTags(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s", state.ID.ValueString())))
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	prior := tftags.PriorOf(ctx, state.Tags, state.TagsAll, req.Private, &resp.Diagnostics).WithCurrent(current)
	updateReq.Tags, _ = tftags.ForUpdate(ctx, defaults, plan.Tags, prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s", state.ID.ValueString())), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update DNS Zone", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	var zone apiDNSZone
	if err := json.Unmarshal(apiResp.Body, &zone); err != nil {
		resp.Diagnostics.AddError("Failed to Parse DNS Zone Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &zone, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Restore the plan-pinned computed values: the PUT response is not
	// assertable against the plan, and overwriting the pin made core reject
	// every apply with "inconsistent result after apply" (audit B1) after the
	// zone had already changed server-side. Two halves of the same failure:
	// the response's derived metadata (recordCount, the delegation NS set) is
	// populated only by GET/list server-side and goes 0/omitted whenever the
	// record backend is degraded mid-update — the paired network-service fix
	// populates the update response too, but stays best-effort by design; and
	// the response's status/serial are snapshots of a zone the platform moves
	// independently (a record mutation flips it PENDING for tens of seconds;
	// Designate bumps the SOA serial outside this update), so a concurrent
	// record write between plan and apply would clobber the pinned value with
	// a transition nobody asked for. GET remains the authoritative read: the
	// next refresh reconciles anything that genuinely changed.
	plan.RecordCount = state.RecordCount
	plan.NameServers = state.NameServers
	plan.Status = state.Status
	plan.Serial = state.Serial

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DNSZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete DNS Zone", err.Error())
		return
	}
}

func (r *dnsZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

// ModifyPlan predicts tags_all: unknown whenever the next write changes the
// platform's tag set, including a change to the provider's default_tags.
func (r *dnsZoneResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
}

// currentTags reads the tags the platform holds right now, at memberPath. An update
// derives the keys it keeps — the ones this configuration does not manage —
// from this rather than from state, which is only as fresh as the last
// refresh (tftags.Prior.WithCurrent).
func (r *dnsZoneResource) currentTags(ctx context.Context, memberPath string) (map[string]string, error) {
	apiResp, err := r.client.Get(ctx, memberPath, nil)
	if err != nil {
		return nil, err
	}
	obj, err := client.ParseResponse[apiDNSZone](apiResp)
	if err != nil {
		return nil, err
	}
	return obj.customerTags(), nil
}
