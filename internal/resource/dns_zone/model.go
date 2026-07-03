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
	Tags        types.Map    `tfsdk:"tags"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// apiDNSZone is the API representation of a DNS zone. Only the fields the
// network service (Designate-backed, ADR-0073) actually persists and echoes
// back are modeled. Zone tags are persisted in a metadata sidecar (ADR-0093)
// and are omitted from the response when empty (never {}). The vpc binding and
// the SOA refresh/retry/expire/minimum internals are still not round-tripped,
// so exposing them would produce perpetual drift.
type apiDNSZone struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Email       string            `json:"email"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type"`
	Status      string            `json:"status"`
	Serial      uint32            `json:"serial"`
	TTL         int               `json:"ttl"`
	RecordCount int               `json:"recordCount"`
	NameServers []string          `json:"nameServers,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt,omitempty"`
}

// apiCreateDNSZoneRequest is the API request to create a DNS zone. The type is
// omitted so the backend defaults it to primary.
type apiCreateDNSZoneRequest struct {
	Name        string            `json:"name"`
	Email       string            `json:"email"`
	Description string            `json:"description,omitempty"`
	TTL         int               `json:"ttl,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiUpdateDNSZoneRequest is the API request to update a DNS zone.
//
// Tags deliberately has NO omitempty: the backend's whole-set-replace semantics
// (ADR-0093) are omit=unchanged, {}=clear, non-empty=replace. Terraform is
// declarative and always knows the full desired tag set, so update always sends
// it — an explicit empty object clears, a non-empty object replaces. With
// omitempty an empty map would be dropped from the wire and read as "unchanged",
// silently defeating a `terraform apply` that removes all tags.
type apiUpdateDNSZoneRequest struct {
	Email       *string           `json:"email,omitempty"`
	Description *string           `json:"description,omitempty"`
	TTL         *int              `json:"ttl,omitempty"`
	Tags        map[string]string `json:"tags"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *DNSZoneModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateDNSZoneRequest {
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
	// Only send tags when the user set them. A null/unset tags attribute omits
	// the field (omitempty) so a zone created without tags round-trips to null;
	// an explicit empty map serializes to nothing (still no tags) and fromAPI
	// preserves the empty map, so neither case drifts.
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}
	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *DNSZoneModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateDNSZoneRequest {
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

	// Always send the full desired tag set (whole-set-replace, ADR-0093).
	// A non-nil empty map serializes to `"tags":{}` which clears server-side;
	// a null/unset attribute is treated as "no tags", so it also clears. This
	// mirrors how email/description are always sent, and keeps state == plan.
	tags := make(map[string]string)
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	}
	req.Tags = tags

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

	// Tags round-trip drift-free against a backend that omits the field when
	// empty (ADR-0093). `tags` is Optional (not Computed), so the post-apply
	// state must equal the configured value exactly — never flip null<->{}.
	switch {
	case len(zone.Tags) > 0:
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, zone.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	case m.Tags.IsNull():
		// Config never set tags; keep null so an unset attribute stays null.
		m.Tags = types.MapNull(types.StringType)
	default:
		// Config set an explicit empty map; the backend omits empty tags on
		// read, so preserve the empty map (not null) to avoid a spurious diff.
		emptyMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = emptyMap
	}
}
