// Package postgres_read_replica implements the frostmoln_postgres_read_replica Terraform resource.
package postgres_read_replica

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// PostgresReadReplicaModel is the Terraform state model for a PostgreSQL read replica.
type PostgresReadReplicaModel struct {
	ID                types.String `tfsdk:"id"`
	InstanceID        types.String `tfsdk:"instance_id"`
	Name              types.String `tfsdk:"name"`
	Status            types.String `tfsdk:"status"`
	PrivateIP         types.String `tfsdk:"private_ip"`
	Port              types.Int64  `tfsdk:"port"`
	ReplicationLagBytes types.Int64  `tfsdk:"replication_lag_bytes"`
}

// apiPostgresReadReplica is the API representation of a PostgreSQL read replica.
type apiPostgresReadReplica struct {
	ID                string `json:"id"`
	InstanceID        string `json:"instanceId"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	PrivateIP         string `json:"privateIp,omitempty"`
	Port              int    `json:"port,omitempty"`
	ReplicationLagBytes int64  `json:"replicationLagBytes,omitempty"`
}

// apiCreatePostgresReadReplicaRequest is the API request to create a read replica.
type apiCreatePostgresReadReplicaRequest struct {
	Name string `json:"name"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *PostgresReadReplicaModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreatePostgresReadReplicaRequest {
	return apiCreatePostgresReadReplicaRequest{
		Name: m.Name.ValueString(),
	}
}

// fromAPI populates the Terraform model from an API response.
func (m *PostgresReadReplicaModel) fromAPI(_ context.Context, replica *apiPostgresReadReplica, _ *diag.Diagnostics) {
	m.ID = types.StringValue(replica.ID)
	m.InstanceID = types.StringValue(replica.InstanceID)
	m.Name = types.StringValue(replica.Name)
	m.Status = types.StringValue(replica.Status)

	if replica.PrivateIP != "" {
		m.PrivateIP = types.StringValue(replica.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	if replica.Port > 0 {
		m.Port = types.Int64Value(int64(replica.Port))
	} else {
		m.Port = types.Int64Null()
	}

	if replica.ReplicationLagBytes > 0 {
		m.ReplicationLagBytes = types.Int64Value(replica.ReplicationLagBytes)
	} else {
		m.ReplicationLagBytes = types.Int64Null()
	}
}
