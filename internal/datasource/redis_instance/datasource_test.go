package redis_instance

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
	if resp.TypeName != "frostmoln_redis_instance" {
		t.Errorf("expected type name frostmoln_redis_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{
		"id", "name", "engine_version", "flavor_id", "vpc_id", "subnet_id",
		"persistence_mode", "eviction_policy", "status", "private_ip", "port",
		"admin_username", "created_at", "updated_at",
	}
	for _, attr := range expectedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}

	// Verify id is required
	idAttr := resp.Schema.Attributes["id"].(schema.StringAttribute)
	if !idAttr.Required {
		t.Error("expected id to be required")
	}

	// Verify computed attrs
	nameAttr := resp.Schema.Attributes["name"].(schema.StringAttribute)
	if !nameAttr.Computed {
		t.Error("expected name to be computed")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &redisInstanceDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &redisInstanceDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, instances map[string]apiRedisInstance) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})

	for id, inst := range instances {
		i := inst
		mux.HandleFunc("/v1/tenants/tenant-1/caches/"+id, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(i)
		})
	}

	return httptest.NewServer(mux)
}

func configureRedisInstanceDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
	t.Helper()
	dc, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatal("datasource does not implement DataSourceWithConfigure")
	}
	configReq := datasource.ConfigureRequest{ProviderData: c}
	var configResp datasource.ConfigureResponse
	dc.Configure(context.Background(), configReq, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}
}

func getRedisInstanceDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadRedisInstanceByID(t *testing.T) {
	instances := map[string]apiRedisInstance{
		"redis-1": {
			ID:              "redis-1",
			Name:            "my-cache",
			EngineVersion:   "7.2",
			FlavorID:        "cache.small",
			VPCID:           "vpc-1",
			SubnetID:        "sub-1",
			PersistenceMode: "rdb",
			EvictionPolicy:  "noeviction",
			Status:          "running",
			PrivateIP:       "10.0.1.10",
			Port:            6379,
			AdminUsername:   "default",
			CreatedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:       "2025-01-02T00:00:00Z",
		},
	}
	server := newTestServer(t, instances)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureRedisInstanceDS(t, ds, c)
	schemaResp := getRedisInstanceDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, "redis-1"),
		"name":             tftypes.NewValue(tftypes.String, nil),
		"engine_version":   tftypes.NewValue(tftypes.String, nil),
		"flavor_id":        tftypes.NewValue(tftypes.String, nil),
		"vpc_id":           tftypes.NewValue(tftypes.String, nil),
		"subnet_id":        tftypes.NewValue(tftypes.String, nil),
		"persistence_mode": tftypes.NewValue(tftypes.String, nil),
		"eviction_policy":  tftypes.NewValue(tftypes.String, nil),
		"status":           tftypes.NewValue(tftypes.String, nil),
		"private_ip":       tftypes.NewValue(tftypes.String, nil),
		"port":             tftypes.NewValue(tftypes.Number, nil),
		"admin_username":   tftypes.NewValue(tftypes.String, nil),
		"created_at":       tftypes.NewValue(tftypes.String, nil),
		"updated_at":       tftypes.NewValue(tftypes.String, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state redisInstanceModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "redis-1" {
		t.Errorf("expected ID redis-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "my-cache" {
		t.Errorf("expected Name my-cache, got %s", state.Name.ValueString())
	}
	if state.EngineVersion.ValueString() != "7.2" {
		t.Errorf("expected EngineVersion 7.2, got %s", state.EngineVersion.ValueString())
	}
	if state.FlavorID.ValueString() != "cache.small" {
		t.Errorf("expected FlavorID cache.small, got %s", state.FlavorID.ValueString())
	}
	if state.PrivateIP.ValueString() != "10.0.1.10" {
		t.Errorf("expected PrivateIP 10.0.1.10, got %s", state.PrivateIP.ValueString())
	}
	if state.Port.ValueInt64() != 6379 {
		t.Errorf("expected Port 6379, got %d", state.Port.ValueInt64())
	}
	if state.PersistenceMode.ValueString() != "rdb" {
		t.Errorf("expected PersistenceMode rdb, got %s", state.PersistenceMode.ValueString())
	}
	if state.EvictionPolicy.ValueString() != "noeviction" {
		t.Errorf("expected EvictionPolicy noeviction, got %s", state.EvictionPolicy.ValueString())
	}
}

func TestTFSDK_ReadRedisInstanceNotFound(t *testing.T) {
	server := newTestServer(t, map[string]apiRedisInstance{})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureRedisInstanceDS(t, ds, c)
	schemaResp := getRedisInstanceDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, "redis-nonexistent"),
		"name":             tftypes.NewValue(tftypes.String, nil),
		"engine_version":   tftypes.NewValue(tftypes.String, nil),
		"flavor_id":        tftypes.NewValue(tftypes.String, nil),
		"vpc_id":           tftypes.NewValue(tftypes.String, nil),
		"subnet_id":        tftypes.NewValue(tftypes.String, nil),
		"persistence_mode": tftypes.NewValue(tftypes.String, nil),
		"eviction_policy":  tftypes.NewValue(tftypes.String, nil),
		"status":           tftypes.NewValue(tftypes.String, nil),
		"private_ip":       tftypes.NewValue(tftypes.String, nil),
		"port":             tftypes.NewValue(tftypes.Number, nil),
		"admin_username":   tftypes.NewValue(tftypes.String, nil),
		"created_at":       tftypes.NewValue(tftypes.String, nil),
		"updated_at":       tftypes.NewValue(tftypes.String, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for nonexistent Redis instance")
	}
}

func TestAPIRedisInstanceSerialization(t *testing.T) {
	inst := apiRedisInstance{
		ID:              "redis-1",
		Name:            "test-cache",
		EngineVersion:   "7.2",
		FlavorID:        "cache.small",
		VPCID:           "vpc-1",
		SubnetID:        "sub-1",
		PersistenceMode: "rdb",
		EvictionPolicy:  "noeviction",
		Status:          "running",
		PrivateIP:       "10.0.1.10",
		Port:            6379,
		AdminUsername:   "default",
		CreatedAt:       "2025-01-01T00:00:00Z",
		UpdatedAt:       "2025-01-02T00:00:00Z",
	}

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiRedisInstance
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != inst.ID {
		t.Errorf("expected ID %s, got %s", inst.ID, decoded.ID)
	}
	if decoded.Name != inst.Name {
		t.Errorf("expected Name %s, got %s", inst.Name, decoded.Name)
	}
	if decoded.EngineVersion != inst.EngineVersion {
		t.Errorf("expected EngineVersion %s, got %s", inst.EngineVersion, decoded.EngineVersion)
	}
	if decoded.FlavorID != inst.FlavorID {
		t.Errorf("expected FlavorID %s, got %s", inst.FlavorID, decoded.FlavorID)
	}
	if decoded.PersistenceMode != inst.PersistenceMode {
		t.Errorf("expected PersistenceMode %s, got %s", inst.PersistenceMode, decoded.PersistenceMode)
	}
	if decoded.EvictionPolicy != inst.EvictionPolicy {
		t.Errorf("expected EvictionPolicy %s, got %s", inst.EvictionPolicy, decoded.EvictionPolicy)
	}
	if decoded.Port != inst.Port {
		t.Errorf("expected Port %d, got %d", inst.Port, decoded.Port)
	}
}
