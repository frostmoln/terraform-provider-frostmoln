package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func dsSchema(t *testing.T) schema.Schema {
	t.Helper()
	d := NewDataSource()
	resp := &datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	return resp.Schema
}

func dsObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":             tftypes.String,
			"vpc_id":         tftypes.String,
			"mode":           tftypes.String,
			"source_address": tftypes.String,
			"status":         tftypes.String,
			"origin":         tftypes.String,
		},
	}
}

func dsConfig(vpcID string) tftypes.Value {
	null := tftypes.NewValue(tftypes.String, nil)
	return tftypes.NewValue(dsObjectType(), map[string]tftypes.Value{
		"id":             null,
		"vpc_id":         tftypes.NewValue(tftypes.String, vpcID),
		"mode":           null,
		"source_address": null,
		"status":         null,
		"origin":         null,
	})
}

func configuredDataSource(t *testing.T, serverURL string) datasource.DataSource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	d := NewDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(
		context.Background(),
		datasource.ConfigureRequest{ProviderData: c},
		&datasource.ConfigureResponse{},
	)
	return d
}

func TestEgressGatewayDataSourceMetadata(t *testing.T) {
	d := NewDataSource()
	resp := &datasource.MetadataResponse{}
	d.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_gateway" {
		t.Errorf("unexpected type name %s", resp.TypeName)
	}
}

func TestEgressGatewayDataSourceConfigureWrongType(t *testing.T) {
	d := NewDataSource()
	resp := &datasource.ConfigureResponse{}
	d.(datasource.DataSourceWithConfigure).Configure(context.Background(),
		datasource.ConfigureRequest{ProviderData: true}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

// TestEgressGatewayDataSourceVPCIDRequired: the lookup is only ever asked for
// one VPC. An Optional vpc_id would make the unfiltered, tenant-wide list
// reachable, and the caller takes element [0].
func TestEgressGatewayDataSourceVPCIDRequired(t *testing.T) {
	s := dsSchema(t)
	attr, ok := s.Attributes["vpc_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected vpc_id to be a StringAttribute, got %T", s.Attributes["vpc_id"])
	}
	if !attr.Required {
		t.Error("vpc_id must be Required")
	}
}

// TestEgressGatewayDataSourceRead reads a gateway still on the WITHDRAWN "nat"
// mode on purpose. The mode can no longer be set, but VPCs are still running on
// it, and a data source that refused or rewrote the value would either break
// every configuration reading such a VPC or report a mode the platform is not
// running. The API's value goes to state verbatim.
func TestEgressGatewayDataSourceRead(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-1","vpcId":"vpc-1","mode":"nat",
			"sourceAddress":"46.246.117.231","status":"active","origin":"vpc_create"}],"totalCount":1}`))
	}))
	defer server.Close()

	d := configuredDataSource(t, server.URL)
	s := dsSchema(t)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: dsConfig("vpc-1")},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotQuery != "vpcId=vpc-1" {
		t.Errorf("expected the lookup to be narrowed to the VPC, got %q", gotQuery)
	}

	var state gatewayModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "gw-1" || state.Mode.ValueString() != "nat" {
		t.Errorf("unexpected state %+v", state)
	}
	if state.SourceAddress.ValueString() != "46.246.117.231" {
		t.Errorf("unexpected source_address %v", state.SourceAddress)
	}
}

func TestEgressGatewayDataSourceReadDetachedSourceAddressIsNull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-1","vpcId":"vpc-1","mode":"public_ip",
			"status":"detached","origin":"legacy"}],"totalCount":1}`))
	}))
	defer server.Close()

	d := configuredDataSource(t, server.URL)
	s := dsSchema(t)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: dsConfig("vpc-1")},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	var state gatewayModel
	resp.State.Get(context.Background(), &state)
	if !state.SourceAddress.IsNull() {
		t.Errorf("a detached gateway has no source address; expected null, got %v", state.SourceAddress)
	}
}

// TestEgressGatewayDataSourceRefusesEmptyVPCID is the guard that matters. An
// interpolation resolving to "" must never reach the API: `?vpcId=` is a 400 by
// design, and dropping the parameter would return the TENANT-WIDE list, from
// which element [0] is some unrelated VPC's gateway — an address a practitioner
// would then put in an allow-list.
func TestEgressGatewayDataSourceRefusesEmptyVPCID(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-other","vpcId":"vpc-other","mode":"public_ip",
			"status":"active","origin":"explicit"}],"totalCount":1}`))
	}))
	defer server.Close()

	d := configuredDataSource(t, server.URL)
	s := dsSchema(t)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: dsConfig("")},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an empty vpc_id must be refused")
	}
	if calls != 0 {
		t.Errorf("no request may be sent for an empty vpc_id; saw %d", calls)
	}
}

func TestEgressGatewayDataSourceNoGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gateways":[],"totalCount":0}`))
	}))
	defer server.Close()

	d := configuredDataSource(t, server.URL)
	s := dsSchema(t)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: dsConfig("vpc-1")},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for a VPC with no gateway")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	// The empty result also covers a VPC id this tenant does not own; asserting
	// only "has no gateway" would send someone hunting a nonexistent bug.
	if !strings.Contains(detail, "does not own") {
		t.Errorf("the diagnostic must not assert the VPC exists, got:\n%s", detail)
	}
}

func TestEgressGatewayDataSourceAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"boom"}}`))
	}))
	defer server.Close()

	d := configuredDataSource(t, server.URL)
	s := dsSchema(t)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: dsConfig("vpc-1")},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the API error to surface")
	}
}
