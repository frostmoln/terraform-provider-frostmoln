// Package postgres_read_replica implements the frostmoln_postgres_read_replica Terraform resource.
package postgres_read_replica

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// PostgresReadReplicaModel is the Terraform state model for a PostgreSQL read replica.
type PostgresReadReplicaModel struct {
	ID                  types.String `tfsdk:"id"`
	InstanceID          types.String `tfsdk:"instance_id"`
	Name                types.String `tfsdk:"name"`
	FlavorID            types.String `tfsdk:"flavor_id"`
	Status              types.String `tfsdk:"status"`
	PrivateIP           types.String `tfsdk:"private_ip"`
	Port                types.Int64  `tfsdk:"port"`
	ReplicationLagBytes types.Int64  `tfsdk:"replication_lag_bytes"`
}

// apiPostgresReadReplica is the API representation of a PostgreSQL read replica.
type apiPostgresReadReplica struct {
	ID string `json:"id"`
	// The backend returns the primary reference as `primaryId` (domain.ReadReplica);
	// the Terraform attribute is `instance_id`. Reading `instanceId` here left it
	// empty on Read → "inconsistent result after apply" + a spurious RequiresReplace
	// on every subsequent plan.
	InstanceID          string `json:"primaryId"`
	Name                string `json:"name"`
	FlavorID            string `json:"flavorId,omitempty"`
	Status              string `json:"status"`
	PrivateIP           string `json:"privateIp,omitempty"`
	Port                int    `json:"port,omitempty"`
	ReplicationLagBytes int64  `json:"replicationLagBytes,omitempty"`
}

// apiCreatePostgresReadReplicaRequest is the API request to create a read replica.
type apiCreatePostgresReadReplicaRequest struct {
	Name string `json:"name"`
	// FlavorID sizes the replica independently of the primary; empty inherits the
	// primary's flavor (omitempty lets the backend apply that default).
	FlavorID string `json:"flavorId,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *PostgresReadReplicaModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreatePostgresReadReplicaRequest {
	return apiCreatePostgresReadReplicaRequest{
		Name:     m.Name.ValueString(),
		FlavorID: m.FlavorID.ValueString(),
	}
}

// fromAPI populates the Terraform model from an API response.
func (m *PostgresReadReplicaModel) fromAPI(_ context.Context, replica *apiPostgresReadReplica, _ *diag.Diagnostics) {
	m.ID = types.StringValue(replica.ID)
	m.InstanceID = types.StringValue(replica.InstanceID)
	m.Name = types.StringValue(replica.Name)
	m.Status = types.StringValue(replica.Status)

	// flavor_id is Optional+Computed: when the customer omits it, the backend
	// resolves + returns the inherited primary flavor, which Terraform stores as
	// the computed value. Always reflect exactly what the API returned.
	if replica.FlavorID != "" {
		m.FlavorID = types.StringValue(replica.FlavorID)
	} else {
		m.FlavorID = types.StringNull()
	}

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
