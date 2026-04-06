// Package scale_group implements the frostmoln_scale_group Terraform resource.
package scale_group

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ScaleGroupModel is the Terraform state model for a scale group.
type ScaleGroupModel struct {
	ID                     types.String `tfsdk:"id"`
	Name                   types.String `tfsdk:"name"`
	LaunchTemplateID       types.String `tfsdk:"launch_template_id"`
	MinSize                types.Int64  `tfsdk:"min_size"`
	MaxSize                types.Int64  `tfsdk:"max_size"`
	DesiredCapacity        types.Int64  `tfsdk:"desired_capacity"`
	CurrentSize            types.Int64  `tfsdk:"current_size"`
	Status                 types.String `tfsdk:"status"`
	SubnetIDs              types.Set    `tfsdk:"subnet_ids"`
	LoadBalancerPoolIDs    types.Set    `tfsdk:"load_balancer_pool_ids"`
	HealthCheckType        types.String `tfsdk:"health_check_type"`
	HealthCheckGracePeriod types.Int64  `tfsdk:"health_check_grace_period"`
	WarmupSeconds          types.Int64  `tfsdk:"warmup_seconds"`
	CooldownSeconds        types.Int64  `tfsdk:"cooldown_seconds"`
	TerminationPolicy      types.String `tfsdk:"termination_policy"`
	Tags                   types.Map    `tfsdk:"tags"`
	CreatedAt              types.String `tfsdk:"created_at"`
	UpdatedAt              types.String `tfsdk:"updated_at"`
}

// apiScaleGroup is the API representation of a scale group.
type apiScaleGroup struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	LaunchTemplateID       string            `json:"launchTemplateId"`
	MinSize                int               `json:"minSize"`
	MaxSize                int               `json:"maxSize"`
	DesiredCapacity        int               `json:"desiredCapacity"`
	CurrentSize            int               `json:"currentSize"`
	Status                 string            `json:"status"`
	SubnetIDs              []string          `json:"subnetIds"`
	LoadBalancerPoolIDs    []string          `json:"loadBalancerPoolIds,omitempty"`
	HealthCheckType        string            `json:"healthCheckType,omitempty"`
	HealthCheckGracePeriod int               `json:"healthCheckGracePeriod,omitempty"`
	WarmupSeconds          int               `json:"warmupSeconds,omitempty"`
	CooldownSeconds        int               `json:"cooldownSeconds,omitempty"`
	TerminationPolicy      string            `json:"terminationPolicy,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
	CreatedAt              string            `json:"createdAt"`
	UpdatedAt              string            `json:"updatedAt,omitempty"`
}

// apiCreateScaleGroupRequest is the API request to create a scale group.
type apiCreateScaleGroupRequest struct {
	Name                   string            `json:"name"`
	LaunchTemplateID       string            `json:"launchTemplateId"`
	MinSize                int               `json:"minSize"`
	MaxSize                int               `json:"maxSize"`
	DesiredCapacity        int               `json:"desiredCapacity"`
	SubnetIDs              []string          `json:"subnetIds"`
	LoadBalancerPoolIDs    []string          `json:"loadBalancerPoolIds,omitempty"`
	HealthCheckType        string            `json:"healthCheckType,omitempty"`
	HealthCheckGracePeriod *int              `json:"healthCheckGracePeriod,omitempty"`
	WarmupSeconds          *int              `json:"warmupSeconds,omitempty"`
	CooldownSeconds        *int              `json:"cooldownSeconds,omitempty"`
	TerminationPolicy      string            `json:"terminationPolicy,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

// apiUpdateScaleGroupRequest is the API request to update a scale group.
type apiUpdateScaleGroupRequest struct {
	Name                   *string           `json:"name,omitempty"`
	LaunchTemplateID       *string           `json:"launchTemplateId,omitempty"`
	MinSize                *int              `json:"minSize,omitempty"`
	MaxSize                *int              `json:"maxSize,omitempty"`
	DesiredCapacity        *int              `json:"desiredCapacity,omitempty"`
	SubnetIDs              []string          `json:"subnetIds,omitempty"`
	LoadBalancerPoolIDs    []string          `json:"loadBalancerPoolIds,omitempty"`
	HealthCheckType        *string           `json:"healthCheckType,omitempty"`
	HealthCheckGracePeriod *int              `json:"healthCheckGracePeriod,omitempty"`
	WarmupSeconds          *int              `json:"warmupSeconds,omitempty"`
	CooldownSeconds        *int              `json:"cooldownSeconds,omitempty"`
	TerminationPolicy      *string           `json:"terminationPolicy,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *ScaleGroupModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateScaleGroupRequest {
	req := apiCreateScaleGroupRequest{
		Name:             m.Name.ValueString(),
		LaunchTemplateID: m.LaunchTemplateID.ValueString(),
		MinSize:          int(m.MinSize.ValueInt64()),
		MaxSize:          int(m.MaxSize.ValueInt64()),
		DesiredCapacity:  int(m.DesiredCapacity.ValueInt64()),
	}

	if !m.SubnetIDs.IsNull() && !m.SubnetIDs.IsUnknown() {
		var ids []string
		diags.Append(m.SubnetIDs.ElementsAs(ctx, &ids, false)...)
		req.SubnetIDs = ids
	}

	if !m.LoadBalancerPoolIDs.IsNull() && !m.LoadBalancerPoolIDs.IsUnknown() {
		var ids []string
		diags.Append(m.LoadBalancerPoolIDs.ElementsAs(ctx, &ids, false)...)
		req.LoadBalancerPoolIDs = ids
	}

	if !m.HealthCheckType.IsNull() && !m.HealthCheckType.IsUnknown() {
		req.HealthCheckType = m.HealthCheckType.ValueString()
	}

	if !m.HealthCheckGracePeriod.IsNull() && !m.HealthCheckGracePeriod.IsUnknown() {
		v := int(m.HealthCheckGracePeriod.ValueInt64())
		req.HealthCheckGracePeriod = &v
	}

	if !m.WarmupSeconds.IsNull() && !m.WarmupSeconds.IsUnknown() {
		v := int(m.WarmupSeconds.ValueInt64())
		req.WarmupSeconds = &v
	}

	if !m.CooldownSeconds.IsNull() && !m.CooldownSeconds.IsUnknown() {
		v := int(m.CooldownSeconds.ValueInt64())
		req.CooldownSeconds = &v
	}

	if !m.TerminationPolicy.IsNull() && !m.TerminationPolicy.IsUnknown() {
		req.TerminationPolicy = m.TerminationPolicy.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *ScaleGroupModel) toUpdateRequest(ctx context.Context, state *ScaleGroupModel, diags *diag.Diagnostics) apiUpdateScaleGroupRequest {
	req := apiUpdateScaleGroupRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.LaunchTemplateID.Equal(state.LaunchTemplateID) {
		v := m.LaunchTemplateID.ValueString()
		req.LaunchTemplateID = &v
	}
	if !m.MinSize.Equal(state.MinSize) {
		v := int(m.MinSize.ValueInt64())
		req.MinSize = &v
	}
	if !m.MaxSize.Equal(state.MaxSize) {
		v := int(m.MaxSize.ValueInt64())
		req.MaxSize = &v
	}
	if !m.DesiredCapacity.Equal(state.DesiredCapacity) {
		v := int(m.DesiredCapacity.ValueInt64())
		req.DesiredCapacity = &v
	}

	if !m.SubnetIDs.Equal(state.SubnetIDs) {
		if !m.SubnetIDs.IsNull() && !m.SubnetIDs.IsUnknown() {
			var ids []string
			diags.Append(m.SubnetIDs.ElementsAs(ctx, &ids, false)...)
			req.SubnetIDs = ids
		} else {
			req.SubnetIDs = []string{}
		}
	}

	if !m.LoadBalancerPoolIDs.Equal(state.LoadBalancerPoolIDs) {
		if !m.LoadBalancerPoolIDs.IsNull() && !m.LoadBalancerPoolIDs.IsUnknown() {
			var ids []string
			diags.Append(m.LoadBalancerPoolIDs.ElementsAs(ctx, &ids, false)...)
			req.LoadBalancerPoolIDs = ids
		} else {
			req.LoadBalancerPoolIDs = []string{}
		}
	}

	if !m.HealthCheckType.Equal(state.HealthCheckType) {
		v := m.HealthCheckType.ValueString()
		req.HealthCheckType = &v
	}

	if !m.HealthCheckGracePeriod.Equal(state.HealthCheckGracePeriod) {
		v := int(m.HealthCheckGracePeriod.ValueInt64())
		req.HealthCheckGracePeriod = &v
	}

	if !m.WarmupSeconds.Equal(state.WarmupSeconds) {
		v := int(m.WarmupSeconds.ValueInt64())
		req.WarmupSeconds = &v
	}

	if !m.CooldownSeconds.Equal(state.CooldownSeconds) {
		v := int(m.CooldownSeconds.ValueInt64())
		req.CooldownSeconds = &v
	}

	if !m.TerminationPolicy.Equal(state.TerminationPolicy) {
		v := m.TerminationPolicy.ValueString()
		req.TerminationPolicy = &v
	}

	if !m.Tags.Equal(state.Tags) {
		if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
			tags := make(map[string]string)
			diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
			req.Tags = tags
		} else {
			req.Tags = map[string]string{}
		}
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *ScaleGroupModel) fromAPI(ctx context.Context, sg *apiScaleGroup, diags *diag.Diagnostics) {
	m.ID = types.StringValue(sg.ID)
	m.Name = types.StringValue(sg.Name)
	m.LaunchTemplateID = types.StringValue(sg.LaunchTemplateID)
	m.MinSize = types.Int64Value(int64(sg.MinSize))
	m.MaxSize = types.Int64Value(int64(sg.MaxSize))
	m.DesiredCapacity = types.Int64Value(int64(sg.DesiredCapacity))
	m.CurrentSize = types.Int64Value(int64(sg.CurrentSize))
	m.Status = types.StringValue(sg.Status)
	m.CreatedAt = types.StringValue(sg.CreatedAt)

	if sg.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(sg.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	if sg.HealthCheckType != "" {
		m.HealthCheckType = types.StringValue(sg.HealthCheckType)
	} else {
		m.HealthCheckType = types.StringNull()
	}

	if sg.HealthCheckGracePeriod > 0 {
		m.HealthCheckGracePeriod = types.Int64Value(int64(sg.HealthCheckGracePeriod))
	} else if !m.HealthCheckGracePeriod.IsNull() {
		m.HealthCheckGracePeriod = types.Int64Value(0)
	} else {
		m.HealthCheckGracePeriod = types.Int64Null()
	}

	if sg.WarmupSeconds > 0 {
		m.WarmupSeconds = types.Int64Value(int64(sg.WarmupSeconds))
	} else if !m.WarmupSeconds.IsNull() {
		m.WarmupSeconds = types.Int64Value(0)
	} else {
		m.WarmupSeconds = types.Int64Null()
	}

	if sg.CooldownSeconds > 0 {
		m.CooldownSeconds = types.Int64Value(int64(sg.CooldownSeconds))
	} else if !m.CooldownSeconds.IsNull() {
		m.CooldownSeconds = types.Int64Value(0)
	} else {
		m.CooldownSeconds = types.Int64Null()
	}

	if sg.TerminationPolicy != "" {
		m.TerminationPolicy = types.StringValue(sg.TerminationPolicy)
	} else {
		m.TerminationPolicy = types.StringNull()
	}

	// Subnet IDs
	if len(sg.SubnetIDs) > 0 {
		subnetSet, d := types.SetValueFrom(ctx, types.StringType, sg.SubnetIDs)
		diags.Append(d...)
		m.SubnetIDs = subnetSet
	} else {
		subnetSet, d := types.SetValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.SubnetIDs = subnetSet
	}

	// Load balancer pool IDs
	if len(sg.LoadBalancerPoolIDs) > 0 {
		lbSet, d := types.SetValueFrom(ctx, types.StringType, sg.LoadBalancerPoolIDs)
		diags.Append(d...)
		m.LoadBalancerPoolIDs = lbSet
	} else if !m.LoadBalancerPoolIDs.IsNull() {
		lbSet, d := types.SetValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.LoadBalancerPoolIDs = lbSet
	} else {
		m.LoadBalancerPoolIDs = types.SetNull(types.StringType)
	}

	// Tags
	if len(sg.Tags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, sg.Tags)
		diags.Append(d...)
		m.Tags = tagMap
	} else if !m.Tags.IsNull() {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
