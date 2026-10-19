// Package iam_policy implements the frostmoln_iam_policy Terraform resource — a
// reusable, customer-authored IAM access policy (ADR-0102). The document is the
// Frostmoln-native access-policy JSON ({schemaVersion, rules[]}); compose it with
// the frostmoln_iam_policy_document data source rather than hand-writing it.
package iam_policy

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// IAMPolicyModel is the Terraform state model for a shared access policy.
type IAMPolicyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Document    types.String `tfsdk:"document"`
	TenantID    types.String `tfsdk:"tenant_id"`
	AuthoredBy  types.String `tfsdk:"authored_by"`
	Version     types.Int64  `tfsdk:"version"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
	// Tags / TagsAll are the policy's customer resource tags (internal/tftags).
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`
}

// apiPolicy is the identity representation of a stored access policy (the bare
// object returned by create/get/update — not an envelope).
type apiPolicy struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenantId,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Document    json.RawMessage `json:"document"`
	AuthoredBy  string          `json:"authoredBy"`
	Version     int             `json:"version"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
	// Tags are the policy's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
}

// apiCreatePolicyRequest authors a shared policy. Tenant + authoredBy are server-set.
type apiCreatePolicyRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Document    json.RawMessage `json:"document"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags on top (a key sent here wins).
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdatePolicyRequest updates a shared policy; a nil field is left unchanged.
type apiUpdatePolicyRequest struct {
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Document    json.RawMessage `json:"document,omitempty"`
	// Tags, when set, replaces the policy's whole tag set ({} clears). Sent ALONE
	// for a tags-only change, which the API writes without touching the document
	// or its version.
	Tags *map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the model to an API create request. document is
// validated as JSON here so an invalid value yields a friendly attribute error
// rather than an opaque json.Marshal failure at the client boundary.
func (m *IAMPolicyModel) toCreateRequest(diags *diag.Diagnostics) apiCreatePolicyRequest {
	doc := m.Document.ValueString()
	if !json.Valid([]byte(doc)) {
		diags.AddAttributeError(path.Root("document"), "Invalid document", "document must be valid JSON (compose it with the frostmoln_iam_policy_document data source)")
		return apiCreatePolicyRequest{}
	}
	req := apiCreatePolicyRequest{
		Name:     m.Name.ValueString(),
		Document: json.RawMessage(doc),
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}
	return req
}

// toUpdateRequest converts the model to an API update request. Name + document
// are always sent (config-authoritative); description is sent as "" to clear it.
func (m *IAMPolicyModel) toUpdateRequest(diags *diag.Diagnostics) apiUpdatePolicyRequest {
	doc := m.Document.ValueString()
	if !json.Valid([]byte(doc)) {
		diags.AddAttributeError(path.Root("document"), "Invalid document", "document must be valid JSON (compose it with the frostmoln_iam_policy_document data source)")
		return apiUpdatePolicyRequest{}
	}
	name := m.Name.ValueString()
	req := apiUpdatePolicyRequest{
		Name:     &name,
		Document: json.RawMessage(doc),
	}
	desc := ""
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc = m.Description.ValueString()
	}
	req.Description = &desc
	return req
}

// fromAPI populates the model from an API response. The document is preserved as
// the config string when it is semantically equal to what the server returned,
// so state == config even if the server reorders keys or reformats whitespace —
// avoiding a "provider produced inconsistent result after apply" error and a
// spurious perpetual diff. On import (document null) the API value is adopted.
func (m *IAMPolicyModel) fromAPI(p *apiPolicy) {
	m.ID = types.StringValue(p.ID)
	m.Name = types.StringValue(p.Name)
	m.TenantID = types.StringValue(p.TenantID)
	m.AuthoredBy = types.StringValue(p.AuthoredBy)
	m.Version = types.Int64Value(int64(p.Version))
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)

	if p.Description != "" {
		m.Description = types.StringValue(p.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	// The server always stores a document. Preserve the config string only when
	// it is semantically equal to what the server returned; otherwise adopt the
	// server value. An empty/absent server document is NOT treated as "no change"
	// — it falls through to a visible diff Terraform re-applies (fail-closed:
	// never report a policy as intact when the server holds none).
	apiDoc := string(p.Document)
	if !m.Document.IsNull() && !m.Document.IsUnknown() && sameServerDoc(m.Document.ValueString(), apiDoc) {
		// Keep the config string — it denotes the same policy.
		return
	}
	m.Document = types.StringValue(apiDoc)
}

// sameJSONDoc reports whether two JSON strings are semantically equal (same
// value tree, ignoring key order and whitespace). Invalid JSON never matches.
func sameJSONDoc(a, b string) bool {
	return sameDoc(a, b, false)
}

// sameServerDoc is sameJSONDoc for a configured document against the one the
// server returned: identity's FRN normalisation is undone on both sides first
// (canonicalPolicyDoc). Only a config-to-server comparison folds; two configs
// that spell an id differently are a real change to send.
func sameServerDoc(config, server string) bool {
	return sameDoc(config, server, true)
}

func sameDoc(a, b string, fold bool) bool {
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		return false
	}
	if fold {
		canonicalPolicyDoc(av)
		canonicalPolicyDoc(bv)
	}
	return reflect.DeepEqual(av, bv)
}

// tagsFromAPI renders the API's tags into `tags` (the keys the prior value names)
// and `tags_all` (every customer key). m.Tags must hold the prior value: the
// configuration on a create or update, the state on a refresh.
func (m *IAMPolicyModel) tagsFromAPI(ctx context.Context, p *apiPolicy, diags *diag.Diagnostics) {
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(p.Tags), m.Tags, diags)
}

// onlyTagsDiffer reports whether an update changes nothing but the tags: then the
// PATCH carries `tags` alone, so the server leaves the document and its version
// untouched (a label never alters a policy).
func onlyTagsDiffer(plan, state *IAMPolicyModel) bool {
	return plan.Name.Equal(state.Name) && plan.Description.Equal(state.Description) &&
		sameJSONDoc(plan.Document.ValueString(), state.Document.ValueString())
}
