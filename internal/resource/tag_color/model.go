// Package tag_color implements the frostmoln_tag_color resource: one tag colour
// rule of an organization. A rule maps a tag key — and optionally one value of
// it — to a display colour in every tenant of the organization. Display only:
// nothing on the platform reads a rule to decide anything.
package tag_color

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings"
)

// TagColorModel is the Terraform state model of one colour rule.
type TagColorModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Key            types.String `tfsdk:"key"`
	Value          types.String `tfsdk:"value"`
	Color          types.String `tfsdk:"color"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// toRequest builds the create / full-replace body. A null `value` is sent as
// JSON null (any value) and "" as "" (exactly the empty value); the two are
// different rules, and the field is always sent because the PUT is a full
// replace in which an omitted value would also mean "any value".
func (m *TagColorModel) toRequest() tagsettings.RuleRequest {
	req := tagsettings.RuleRequest{
		Key:   m.Key.ValueString(),
		Color: m.Color.ValueString(),
	}
	if !m.Value.IsNull() && !m.Value.IsUnknown() {
		v := m.Value.ValueString()
		req.Value = &v
	}
	return req
}

// fromAPI copies the rule the server returned into the model. Everything is
// taken exactly as returned — the colour's letter case included, which the
// platform keeps as written — so state is what a later read returns and a
// refresh finds no drift the configuration did not cause. organization_id is
// not part of the rule's representation and is left as the caller set it.
func (m *TagColorModel) fromAPI(r *tagsettings.Rule) {
	m.ID = types.StringValue(r.ID)
	m.Key = types.StringValue(r.Key)
	if r.Value == nil {
		m.Value = types.StringNull()
	} else {
		m.Value = types.StringValue(*r.Value)
	}
	m.Color = types.StringValue(r.Color)
	m.CreatedAt = types.StringValue(r.CreatedAt)
	m.UpdatedAt = types.StringValue(r.UpdatedAt)
}
