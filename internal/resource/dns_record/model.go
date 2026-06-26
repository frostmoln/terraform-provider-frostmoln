// Package dns_record implements the frostmoln_dns_record Terraform resource.
package dns_record

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// DNSRecordModel is the Terraform state model for a DNS record (recordset).
type DNSRecordModel struct {
	ID        types.String `tfsdk:"id"`
	ZoneID    types.String `tfsdk:"zone_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Records   types.Set    `tfsdk:"records"`
	TTL       types.Int64  `tfsdk:"ttl"`
	Comment   types.String `tfsdk:"comment"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// apiDNSRecord is the API representation of a DNS recordset. The ID is the stable
// backend recordset UUID. Tags are not round-tripped by Designate (the system of
// record, ADR-0073), so they are not modeled.
type apiDNSRecord struct {
	ID        string   `json:"id"`
	ZoneID    string   `json:"zoneId"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Records   []string `json:"records"`
	TTL       int      `json:"ttl"`
	Comment   string   `json:"comment,omitempty"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

// apiCreateDNSRecordRequest is the API request to create a recordset.
type apiCreateDNSRecordRequest struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Records []string `json:"records"`
	TTL     int      `json:"ttl,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

// apiUpdateDNSRecordRequest is the API request to update a recordset. A non-nil
// Records fully replaces the value set.
type apiUpdateDNSRecordRequest struct {
	Records []string `json:"records,omitempty"`
	TTL     *int     `json:"ttl,omitempty"`
	Comment *string  `json:"comment,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *DNSRecordModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateDNSRecordRequest {
	req := apiCreateDNSRecordRequest{
		Name: m.Name.ValueString(),
		Type: m.Type.ValueString(),
	}

	var values []string
	diags.Append(m.Records.ElementsAs(ctx, &values, false)...)
	req.Records = values

	if !m.TTL.IsNull() && !m.TTL.IsUnknown() {
		req.TTL = int(m.TTL.ValueInt64())
	}
	if !m.Comment.IsNull() && !m.Comment.IsUnknown() {
		req.Comment = m.Comment.ValueString()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *DNSRecordModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateDNSRecordRequest {
	req := apiUpdateDNSRecordRequest{}

	var values []string
	diags.Append(m.Records.ElementsAs(ctx, &values, false)...)
	req.Records = values

	if !m.TTL.IsNull() && !m.TTL.IsUnknown() {
		ttl := int(m.TTL.ValueInt64())
		req.TTL = &ttl
	}

	if !m.Comment.IsNull() && !m.Comment.IsUnknown() {
		comment := m.Comment.ValueString()
		req.Comment = &comment
	} else {
		empty := ""
		req.Comment = &empty
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *DNSRecordModel) fromAPI(ctx context.Context, rec *apiDNSRecord, diags *diag.Diagnostics) {
	m.ID = types.StringValue(rec.ID)
	m.ZoneID = types.StringValue(rec.ZoneID)
	m.Name = types.StringValue(rec.Name)
	m.Type = types.StringValue(rec.Type)
	m.TTL = types.Int64Value(int64(rec.TTL))
	m.CreatedAt = types.StringValue(rec.CreatedAt)

	values, d := types.SetValueFrom(ctx, types.StringType, rec.Records)
	diags.Append(d...)
	m.Records = values

	if rec.Comment != "" {
		m.Comment = types.StringValue(rec.Comment)
	} else {
		m.Comment = types.StringNull()
	}

	if rec.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(rec.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}
}
