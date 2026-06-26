// Package dns_zone implements the frostmoln_dns_zone Terraform resource.
package dns_zone

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// DNSZoneModel is the Terraform state model for a managed DNS zone.
type DNSZoneModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Email       types.String `tfsdk:"email"`
	Description types.String `tfsdk:"description"`
	TTL         types.Int64  `tfsdk:"ttl"`
	Type        types.String `tfsdk:"type"`
	Status      types.String `tfsdk:"status"`
	Serial      types.Int64  `tfsdk:"serial"`
	RecordCount types.Int64  `tfsdk:"record_count"`
	NameServers types.List   `tfsdk:"name_servers"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// apiDNSZone is the API representation of a DNS zone. Only the fields Designate
// (the system of record, ADR-0073) actually persists and echoes back are
// modeled — tags, vpc binding and the SOA refresh/retry/expire/minimum internals
// are not round-tripped, so exposing them would produce perpetual drift.
type apiDNSZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Serial      uint32   `json:"serial"`
	TTL         int      `json:"ttl"`
	RecordCount int      `json:"recordCount"`
	NameServers []string `json:"nameServers,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
}

// apiCreateDNSZoneRequest is the API request to create a DNS zone. The type is
// omitted so the backend defaults it to primary.
type apiCreateDNSZoneRequest struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Description string `json:"description,omitempty"`
	TTL         int    `json:"ttl,omitempty"`
}

// apiUpdateDNSZoneRequest is the API request to update a DNS zone.
type apiUpdateDNSZoneRequest struct {
	Email       *string `json:"email,omitempty"`
	Description *string `json:"description,omitempty"`
	TTL         *int    `json:"ttl,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *DNSZoneModel) toCreateRequest() apiCreateDNSZoneRequest {
	req := apiCreateDNSZoneRequest{
		Name:  m.Name.ValueString(),
		Email: m.Email.ValueString(),
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}
	if !m.TTL.IsNull() && !m.TTL.IsUnknown() {
		req.TTL = int(m.TTL.ValueInt64())
	}
	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *DNSZoneModel) toUpdateRequest() apiUpdateDNSZoneRequest {
	req := apiUpdateDNSZoneRequest{}

	email := m.Email.ValueString()
	req.Email = &email

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc := m.Description.ValueString()
		req.Description = &desc
	} else {
		empty := ""
		req.Description = &empty
	}

	if !m.TTL.IsNull() && !m.TTL.IsUnknown() {
		ttl := int(m.TTL.ValueInt64())
		req.TTL = &ttl
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *DNSZoneModel) fromAPI(ctx context.Context, zone *apiDNSZone, diags *diag.Diagnostics) {
	m.ID = types.StringValue(zone.ID)
	m.Name = types.StringValue(zone.Name)
	m.Email = types.StringValue(zone.Email)
	m.Type = types.StringValue(zone.Type)
	m.Status = types.StringValue(zone.Status)
	m.Serial = types.Int64Value(int64(zone.Serial))
	m.TTL = types.Int64Value(int64(zone.TTL))
	m.RecordCount = types.Int64Value(int64(zone.RecordCount))
	m.CreatedAt = types.StringValue(zone.CreatedAt)

	if zone.Description != "" {
		m.Description = types.StringValue(zone.Description)
	} else {
		m.Description = types.StringNull()
	}

	if zone.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(zone.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	nsList, d := types.ListValueFrom(ctx, types.StringType, zone.NameServers)
	diags.Append(d...)
	m.NameServers = nsList
}
