// Package container_registry_retention implements the
// frostmoln_container_registry_retention Terraform resource — the tenant's
// single tag-retention policy on its repository namespace.
package container_registry_retention

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// RetentionModel is the Terraform state model. ID is the tenant id: there is
// exactly one policy per tenant and it has no identifier of its own.
type RetentionModel struct {
	ID                      types.String `tfsdk:"id"`
	KeepLastTagged          types.Int64  `tfsdk:"keep_last_tagged"`
	DeleteUntaggedAfterDays types.Int64  `tfsdk:"delete_untagged_after_days"`
}

// apiRetentionPolicy is the GET/PUT response body.
type apiRetentionPolicy struct {
	// Configured is a POINTER so an absent field is not read as "no policy":
	// Read removes the resource on false, so a decoded zero value would drop
	// it from state on any envelope drift.
	Configured              *bool  `json:"configured"`
	KeepLastTagged          *int64 `json:"keepLastTagged,omitempty"`
	DeleteUntaggedAfterDays *int64 `json:"deleteUntaggedAfterDays,omitempty"`
}

// apiRetentionInput is the PUT body. PUT REPLACES the policy and an omitted
// field clears that rule, so a null attribute is sent as an omitted field.
type apiRetentionInput struct {
	KeepLastTagged          *int64 `json:"keepLastTagged,omitempty"`
	DeleteUntaggedAfterDays *int64 `json:"deleteUntaggedAfterDays,omitempty"`
}

func (m *RetentionModel) toAPI() apiRetentionInput {
	return apiRetentionInput{
		KeepLastTagged:          m.KeepLastTagged.ValueInt64Pointer(),
		DeleteUntaggedAfterDays: m.DeleteUntaggedAfterDays.ValueInt64Pointer(),
	}
}

func (m *RetentionModel) fromAPI(tenantID string, p *apiRetentionPolicy) {
	m.ID = types.StringValue(tenantID)
	m.KeepLastTagged = types.Int64PointerValue(p.KeepLastTagged)
	m.DeleteUntaggedAfterDays = types.Int64PointerValue(p.DeleteUntaggedAfterDays)
}
