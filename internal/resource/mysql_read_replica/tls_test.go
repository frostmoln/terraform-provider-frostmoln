package mysql_read_replica

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tlsattr/tlsattrtest"
)

// Phase 4c: a MySQL replica's own hostname and certificate state, and its primary's
// requirement, read back as the service sends them; all read-only, and a NULL prior
// (state from an older provider) plans unknown, never null.
func TestSchemaTLSAttributesComputedOnly(t *testing.T) {
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"require_tls", "tls_enabled", "hostname"} {
		tlsattrtest.AssertPinnedComputed(t, resp.Schema.Attributes, name)
	}
}

func TestMysqlReadReplicaModelFromAPITLS(t *testing.T) {
	var model MysqlReadReplicaModel
	diags := diag.Diagnostics{}
	model.fromAPI(context.Background(), &apiMysqlReadReplica{
		ID: "rr-1", InstanceID: "db-1", Name: "ro", Status: "running", Port: 3306,
		RequireTLS: true, TLSEnabled: true, Hostname: "ro.db.prod.omkcmw.ck4to7f6.fm.internal",
	}, &diags)
	if !model.RequireTLS.ValueBool() || !model.TLSEnabled.ValueBool() || model.Hostname.ValueString() != "ro.db.prod.omkcmw.ck4to7f6.fm.internal" {
		t.Errorf("TLS read: require_tls=%v tls_enabled=%v hostname=%v", model.RequireTLS, model.TLSEnabled, model.Hostname)
	}
	// A replica of a primary built before TLS (or a service that predates the fields).
	model = MysqlReadReplicaModel{}
	model.fromAPI(context.Background(), &apiMysqlReadReplica{ID: "rr-1", InstanceID: "db-1", Name: "ro", Status: "running"}, &diags)
	if model.RequireTLS.IsNull() || model.RequireTLS.ValueBool() || model.TLSEnabled.IsNull() || model.TLSEnabled.ValueBool() || !model.Hostname.IsNull() {
		t.Errorf("pre-TLS replica: require_tls=%v tls_enabled=%v hostname=%v", model.RequireTLS, model.TLSEnabled, model.Hostname)
	}
}
