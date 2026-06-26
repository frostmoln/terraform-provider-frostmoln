// Package messaging_instance implements the frostmoln_messaging_instance Terraform resource.
package messaging_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// MessagingInstanceModel is the Terraform state model for a managed messaging instance.
type MessagingInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Engine          types.String `tfsdk:"engine"`
	EngineVersion   types.String `tfsdk:"engine_version"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	VPCID           types.String `tfsdk:"vpc_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	PersistenceMode types.String `tfsdk:"persistence_mode"`
	Status          types.String `tfsdk:"status"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	Port            types.Int64  `tfsdk:"port"`
	AMQPSPort       types.Int64  `tfsdk:"amqps_port"`
	ManagementPort  types.Int64  `tfsdk:"management_port"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
}

// apiMessagingInstance is the API representation of a managed messaging instance.
type apiMessagingInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	AMQPSPort       int    `json:"amqpsPort,omitempty"`
	ManagementPort  int    `json:"managementPort,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// apiCreateMessagingInstanceRequest is the API request to create a managed messaging instance.
type apiCreateMessagingInstanceRequest struct {
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode,omitempty"`
}

// apiUpdateMessagingInstanceRequest is the API request to update a managed messaging instance.
type apiUpdateMessagingInstanceRequest struct {
	Name            *string `json:"name,omitempty"`
	FlavorID        *string `json:"flavorId,omitempty"`
	PersistenceMode *string `json:"persistenceMode,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *MessagingInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateMessagingInstanceRequest {
	req := apiCreateMessagingInstanceRequest{
		Name:          m.Name.ValueString(),
		Engine:        m.Engine.ValueString(),
		EngineVersion: m.EngineVersion.ValueString(),
		FlavorID:      m.FlavorID.ValueString(),
		VPCID:         m.VPCID.ValueString(),
		SubnetID:      m.SubnetID.ValueString(),
	}

	if !m.PersistenceMode.IsNull() && !m.PersistenceMode.IsUnknown() {
		req.PersistenceMode = m.PersistenceMode.ValueString()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *MessagingInstanceModel) toUpdateRequest(state *MessagingInstanceModel) apiUpdateMessagingInstanceRequest {
	req := apiUpdateMessagingInstanceRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.FlavorID.Equal(state.FlavorID) {
		v := m.FlavorID.ValueString()
		req.FlavorID = &v
	}
	if !m.PersistenceMode.Equal(state.PersistenceMode) {
		v := m.PersistenceMode.ValueString()
		req.PersistenceMode = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *MessagingInstanceModel) fromAPI(_ context.Context, inst *apiMessagingInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Engine = types.StringValue(inst.Engine)
	m.EngineVersion = types.StringValue(inst.EngineVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.PersistenceMode = types.StringValue(inst.PersistenceMode)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.PrivateIP != "" {
		m.PrivateIP = types.StringValue(inst.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	if inst.Port > 0 {
		m.Port = types.Int64Value(int64(inst.Port))
	} else {
		m.Port = types.Int64Null()
	}

	if inst.AMQPSPort > 0 {
		m.AMQPSPort = types.Int64Value(int64(inst.AMQPSPort))
	} else {
		m.AMQPSPort = types.Int64Null()
	}

	if inst.ManagementPort > 0 {
		m.ManagementPort = types.Int64Value(int64(inst.ManagementPort))
	} else {
		m.ManagementPort = types.Int64Null()
	}

	if inst.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}
}
