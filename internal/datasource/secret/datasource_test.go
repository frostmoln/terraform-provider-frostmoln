package secret

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
	if resp.TypeName != "frostmoln_secret" {
		t.Errorf("expected type name frostmoln_secret, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	expectedAttrs := []string{
		"id", "name", "description", "content_type", "tags",
		"max_versions", "recovery_window_days", "current_version",
		"status", "created_at", "updated_at",
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
	tagsAttr := resp.Schema.Attributes["tags"].(schema.MapAttribute)
	if !tagsAttr.Computed {
		t.Error("expected tags to be computed")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &secretDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &secretDataSource{}
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

func configureSecretDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
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

func getSecretDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp
}

func secretConfigVal(t *testing.T, id string) tftypes.Value {
	t.Helper()
	schemaResp := getSecretDSSchema(t)
	tfType := schemaResp.Schema.Type().TerraformType(context.Background())
	idVal := tftypes.NewValue(tftypes.String, nil)
	if id != "" {
		idVal = tftypes.NewValue(tftypes.String, id)
	}
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                   idVal,
		"name":                 tftypes.NewValue(tftypes.String, nil),
		"description":          tftypes.NewValue(tftypes.String, nil),
		"content_type":         tftypes.NewValue(tftypes.String, nil),
		"tags":                 tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"max_versions":         tftypes.NewValue(tftypes.Number, nil),
		"recovery_window_days": tftypes.NewValue(tftypes.Number, nil),
		"current_version":      tftypes.NewValue(tftypes.Number, nil),
		"status":               tftypes.NewValue(tftypes.String, nil),
		"created_at":           tftypes.NewValue(tftypes.String, nil),
		"updated_at":           tftypes.NewValue(tftypes.String, nil),
	})
}

func TestReadByID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/secrets/sec-1" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiSecret{
				ID:                 "sec-1",
				Name:               "my-secret",
				Description:        "a description",
				ContentType:        "text/plain",
				Tags:               map[string]string{"env": "prod"},
				MaxVersions:        10,
				RecoveryWindowDays: 7,
				CurrentVersion:     3,
				Status:             "active",
				CreatedAt:          "2025-01-01T00:00:00Z",
				UpdatedAt:          "2025-01-02T00:00:00Z",
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
	configureSecretDS(t, ds, c)
	schemaResp := getSecretDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: secretConfigVal(t, "sec-1")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state secretModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "sec-1" {
		t.Errorf("expected ID sec-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "my-secret" {
		t.Errorf("expected Name my-secret, got %s", state.Name.ValueString())
	}
	if state.Description.ValueString() != "a description" {
		t.Errorf("expected Description, got %s", state.Description.ValueString())
	}
	if state.ContentType.ValueString() != "text/plain" {
		t.Errorf("expected ContentType text/plain, got %s", state.ContentType.ValueString())
	}
	if state.MaxVersions.ValueInt64() != 10 {
		t.Errorf("expected MaxVersions 10, got %d", state.MaxVersions.ValueInt64())
	}
	if state.CurrentVersion.ValueInt64() != 3 {
		t.Errorf("expected CurrentVersion 3, got %d", state.CurrentVersion.ValueInt64())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", state.Status.ValueString())
	}
	if state.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected UpdatedAt, got %s", state.UpdatedAt.ValueString())
	}
	tags := state.Tags.Elements()
	if len(tags) != 1 {
		t.Errorf("expected 1 tag, got %d", len(tags))
	}
}

func TestReadByIDNullableFieldsEmpty(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/secrets/sec-2" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiSecret{
				ID:                 "sec-2",
				Name:               "minimal",
				ContentType:        "text/plain",
				MaxVersions:        5,
				RecoveryWindowDays: 7,
				CurrentVersion:     1,
				Status:             "active",
				CreatedAt:          "2025-01-01T00:00:00Z",
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
	configureSecretDS(t, ds, c)
	schemaResp := getSecretDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: secretConfigVal(t, "sec-2")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state secretModel
	readResp.State.Get(ctx, &state)

	if !state.Description.IsNull() {
		t.Error("expected null description")
	}
	if !state.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
	if !state.Tags.IsNull() {
		t.Error("expected null tags")
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
	configureSecretDS(t, ds, c)
	schemaResp := getSecretDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: secretConfigVal(t, "sec-err")},
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
	configureSecretDS(t, ds, c)
	schemaResp := getSecretDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: secretConfigVal(t, "sec-bad")},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}
