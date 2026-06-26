package dns_record

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &dnsRecordResource{}
	_ resource.ResourceWithImportState = &dnsRecordResource{}
)

type dnsRecordResource struct {
	client *client.Client
}

// NewResource returns a new DNS record resource.
func NewResource() resource.Resource {
	return &dnsRecordResource{}
}

func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a DNS record (recordset) within a Frostmoln managed DNS zone. " +
			"A recordset is one (name, type) pair with a single TTL and one or more values.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the recordset. Stable across value and TTL edits.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone_id": schema.StringAttribute{
				Description: "The ID of the zone this record belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The record name relative to the zone, or \"@\" for the zone apex. Must be lowercase.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[^A-Z]+$`),
						"must be lowercase (the backend lowercases record names)",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Description: "The record type: A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, or PTR.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "PTR"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"records": schema.SetAttribute{
				Description: "The record values. For MX/SRV the priority is part of the value string (e.g. \"10 mail.example.com.\").",
				Required:    true,
				ElementType: types.StringType,
			},
			"ttl": schema.Int64Attribute{
				Description: "The record TTL in seconds.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"comment": schema.StringAttribute{
				Description: "A comment for the record.",
				Optional:    true,
			},
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

func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DNSRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := plan.ZoneID.ValueString()
	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// DNS is served synchronously by the network service (ADR-0073) — create
	// returns the recordset directly, no async operation.
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s/records", zoneID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create DNS Record", err.Error())
		return
	}

	var rec apiDNSRecord
	if err := json.Unmarshal(apiResp.Body, &rec); err != nil {
		resp.Diagnostics.AddError("Failed to Parse DNS Record Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &rec, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DNSRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := state.ZoneID.ValueString()
	recordID := state.ID.ValueString()

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s/records/%s", zoneID, recordID)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read DNS Record", err.Error())
		return
	}

	var rec apiDNSRecord
	if err := json.Unmarshal(apiResp.Body, &rec); err != nil {
		resp.Diagnostics.AddError("Failed to Parse DNS Record Response", err.Error())
		return
	}

	state.fromAPI(ctx, &rec, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DNSRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state DNSRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := state.ZoneID.ValueString()
	recordID := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s/records/%s", zoneID, recordID)), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update DNS Record", err.Error())
		return
	}

	var rec apiDNSRecord
	if err := json.Unmarshal(apiResp.Body, &rec); err != nil {
		resp.Diagnostics.AddError("Failed to Parse DNS Record Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &rec, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DNSRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := state.ZoneID.ValueString()
	recordID := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/dns/zones/%s/records/%s", zoneID, recordID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete DNS Record", err.Error())
		return
	}
}

func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {zone_id}/{record_id}
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {zone_id}/{record_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
