package dns_record

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &dnsRecordResource{}
	_ resource.ResourceWithImportState = &dnsRecordResource{}
	_ resource.ResourceWithModifyPlan  = &dnsRecordResource{}
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
			"A recordset is one (name, type) pair with a single TTL and one or more values." +
			"\n\n" + scopedecl.Summary("frostmoln_dns_record"),
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
			"tags": tftags.OfferTagsAttribute("Resource tags on the recordset: your own key/value metadata for " +
				"search and filtering (`?tag=key=value` in the API, `--tag` in the fm CLI), kept by the platform " +
				"and never published in DNS. Changing only the tags updates the recordset in place without " +
				"writing the zone: no serial bump, no change to what resolvers see."),
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
	defaults := r.client.DefaultTags()
	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	createReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
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
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

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
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
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
	recordPath := r.client.TenantPath(fmt.Sprintf("/dns/zones/%s/records/%s", zoneID, recordID))

	// The record's own fields go out only when one of them changed, so a tags
	// change alone never writes the zone.
	var updateReq apiUpdateDNSRecordRequest
	if plan.recordFieldsChanged(&state) {
		updateReq = plan.toUpdateRequest(ctx, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := r.get(ctx, recordPath)
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
	updateReq.Tags = tags

	var rec *apiDNSRecord
	if updateReq.Records != nil || updateReq.TTL != nil || updateReq.Comment != nil || updateReq.Tags != nil {
		apiResp, err := r.client.Put(ctx, recordPath, updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Update DNS Record", err.Error())
			return
		}
		rec = &apiDNSRecord{}
		if err := json.Unmarshal(apiResp.Body, rec); err != nil {
			resp.Diagnostics.AddError("Failed to Parse DNS Record Response", err.Error())
			return
		}
	} else if rec, err = r.get(ctx, recordPath); err != nil {
		// Nothing to write (a default_tags change the platform already matches):
		// read what it holds.
		resp.Diagnostics.AddError("Failed to Read DNS Record After Update", err.Error())
		return
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it; that also ends an import's pending state.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	plan.fromAPI(ctx, rec, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// get reads one recordset.
func (r *dnsRecordResource) get(ctx context.Context, recordPath string) (*apiDNSRecord, error) {
	apiResp, err := r.client.Get(ctx, recordPath, nil)
	if err != nil {
		return nil, err
	}
	var rec apiDNSRecord
	if err := json.Unmarshal(apiResp.Body, &rec); err != nil {
		return nil, fmt.Errorf("failed to parse DNS record response: %w", err)
	}
	return &rec, nil
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the recordset's tags.
func (r *dnsRecordResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
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
	parts, err := client.ParseImportID(req.ID, "zone_id", "record_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}
