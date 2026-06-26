package messaging_instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestNewDataSource(t *testing.T) {
	ds := NewDataSource()
	if ds == nil {
		t.Fatal("expected non-nil data source")
	}
}

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_messaging_instance" {
		t.Errorf("expected type name frostmoln_messaging_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	expectedAttrs := []string{
		"id", "name", "engine", "engine_version", "flavor_id", "vpc_id",
		"subnet_id", "persistence_mode", "status", "private_ip", "port",
		"amqps_port", "management_port", "created_at", "updated_at",
	}
	for _, attr := range expectedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}
	idAttr := resp.Schema.Attributes["id"].(schema.StringAttribute)
	if !idAttr.Required {
		t.Error("expected id to be required")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &messagingInstanceDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &messagingInstanceDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- helpers ---

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"})
	})
	mux.HandleFunc("/", handler)
	return httptest.NewServer(mux)
}

func configureDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
	t.Helper()
	dc, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatal("datasource does not implement DataSourceWithConfigure")
	}
	var resp datasource.ConfigureResponse
	dc.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", resp.Diagnostics.Errors())
	}
}

func getDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp
}

func configVal(t *testing.T, id string) tftypes.Value {
	t.Helper()
	schemaResp := getDSSchema(t)
	tfType := schemaResp.Schema.Type().TerraformType(context.Background())
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, id),
		"name":             tftypes.NewValue(tftypes.String, nil),
		"engine":           tftypes.NewValue(tftypes.String, nil),
		"engine_version":   tftypes.NewValue(tftypes.String, nil),
		"flavor_id":        tftypes.NewValue(tftypes.String, nil),
		"vpc_id":           tftypes.NewValue(tftypes.String, nil),
		"subnet_id":        tftypes.NewValue(tftypes.String, nil),
		"persistence_mode": tftypes.NewValue(tftypes.String, nil),
		"status":           tftypes.NewValue(tftypes.String, nil),
		"private_ip":       tftypes.NewValue(tftypes.String, nil),
		"port":             tftypes.NewValue(tftypes.Number, nil),
		"amqps_port":       tftypes.NewValue(tftypes.Number, nil),
		"management_port":  tftypes.NewValue(tftypes.Number, nil),
		"created_at":       tftypes.NewValue(tftypes.String, nil),
		"updated_at":       tftypes.NewValue(tftypes.String, nil),
	})
}

func TestReadByID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/messaging/mq-1" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-1",
				Name:            "my-broker",
				Engine:          "lavinmq",
				EngineVersion:   "2.3",
				FlavorID:        "mq.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "persistent",
				Status:          "running",
				PrivateIP:       "10.0.1.7",
				Port:            5672,
				AMQPSPort:       5671,
				ManagementPort:  15672,
				CreatedAt:       "2025-01-01T00:00:00Z",
				UpdatedAt:       "2025-01-02T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal(t, "mq-1")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state messagingInstanceModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "mq-1" {
		t.Errorf("expected ID mq-1, got %s", state.ID.ValueString())
	}
	if state.Engine.ValueString() != "lavinmq" {
		t.Errorf("expected Engine lavinmq, got %s", state.Engine.ValueString())
	}
	if state.EngineVersion.ValueString() != "2.3" {
		t.Errorf("expected EngineVersion 2.3, got %s", state.EngineVersion.ValueString())
	}
	if state.FlavorID.ValueString() != "mq.gp1.small" {
		t.Errorf("expected FlavorID mq.gp1.small, got %s", state.FlavorID.ValueString())
	}
	if state.Port.ValueInt64() != 5672 {
		t.Errorf("expected Port 5672, got %d", state.Port.ValueInt64())
	}
	if state.AMQPSPort.ValueInt64() != 5671 {
		t.Errorf("expected AMQPSPort 5671, got %d", state.AMQPSPort.ValueInt64())
	}
	if state.ManagementPort.ValueInt64() != 15672 {
		t.Errorf("expected ManagementPort 15672, got %d", state.ManagementPort.ValueInt64())
	}
}

func TestReadByIDNullableFieldsEmpty(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/messaging/mq-2" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-2",
				Name:            "minimal",
				Engine:          "lavinmq",
				EngineVersion:   "2.3",
				FlavorID:        "mq.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "persistent",
				Status:          "creating",
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal(t, "mq-2")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state messagingInstanceModel
	readResp.State.Get(ctx, &state)

	if !state.PrivateIP.IsNull() {
		t.Error("expected null private_ip")
	}
	if !state.Port.IsNull() {
		t.Error("expected null port")
	}
	if !state.AMQPSPort.IsNull() {
		t.Error("expected null amqps_port")
	}
	if !state.ManagementPort.IsNull() {
		t.Error("expected null management_port")
	}
	if !state.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
}

func TestReadAPIError(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "INTERNAL_ERROR", "message": "server error",
		})
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal(t, "mq-err")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}

func TestReadBadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal(t, "mq-bad")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}
