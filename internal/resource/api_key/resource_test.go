package api_key

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestAPIKeyModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	scopes, d := types.ListValueFrom(ctx, types.StringType, []string{"compute:read", "compute:write"})
	diags.Append(d...)

	model := APIKeyModel{
		Name:        types.StringValue("my-key"),
		Description: types.StringValue("Test API key"),
		Scopes:      scopes,
		ExpiresAt:   types.StringValue("2026-12-31T23:59:59Z"),
		RateLimit:   types.Int64Value(100),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "my-key" {
		t.Errorf("expected name my-key, got %s", req.Name)
	}
	if req.Description != "Test API key" {
		t.Errorf("expected description 'Test API key', got %s", req.Description)
	}
	if len(req.Scopes) != 2 {
		t.Errorf("expected 2 scopes, got %d", len(req.Scopes))
	}
	if req.ExpiresAt != "2026-12-31T23:59:59Z" {
		t.Errorf("expected expires_at, got %s", req.ExpiresAt)
	}
	if req.RateLimit != 100 {
		t.Errorf("expected rate_limit 100, got %d", req.RateLimit)
	}
}

func TestAPIKeyModelToCreateRequestMinimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := APIKeyModel{
		Name:        types.StringValue("basic-key"),
		Description: types.StringNull(),
		Scopes:      types.ListNull(types.StringType),
		ExpiresAt:   types.StringNull(),
		RateLimit:   types.Int64Null(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "basic-key" {
		t.Errorf("expected name basic-key, got %s", req.Name)
	}
	if req.Description != "" {
		t.Errorf("expected empty description, got %s", req.Description)
	}
	if req.Scopes != nil {
		t.Errorf("expected nil scopes, got %v", req.Scopes)
	}
	if req.RateLimit != 0 {
		t.Errorf("expected zero rate_limit, got %d", req.RateLimit)
	}
}

func TestAPIKeyModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	scopes, d := types.ListValueFrom(ctx, types.StringType, []string{"compute:read"})
	diags.Append(d...)

	model := APIKeyModel{
		Name:        types.StringValue("renamed-key"),
		Description: types.StringValue("Updated description"),
		Scopes:      scopes,
		RateLimit:   types.Int64Value(200),
	}

	req := model.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name == nil || *req.Name != "renamed-key" {
		t.Errorf("expected name renamed-key, got %v", req.Name)
	}
	if req.Description == nil || *req.Description != "Updated description" {
		t.Errorf("expected description 'Updated description', got %v", req.Description)
	}
	if len(req.Scopes) != 1 || req.Scopes[0] != "compute:read" {
		t.Errorf("expected scope compute:read, got %v", req.Scopes)
	}
	if req.RateLimit == nil || *req.RateLimit != 200 {
		t.Errorf("expected rate_limit 200, got %v", req.RateLimit)
	}
}

func TestAPIKeyModelToUpdateRequestClearDescription(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := APIKeyModel{
		Name:        types.StringValue("key"),
		Description: types.StringNull(), // clearing the description
		Scopes:      types.ListNull(types.StringType),
		RateLimit:   types.Int64Null(),
	}

	req := model.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	// Description should be set to empty string to clear it.
	if req.Description == nil || *req.Description != "" {
		t.Errorf("expected empty description to clear, got %v", req.Description)
	}
}

func TestAPIKeyModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	key := &apiAPIKey{
		ID:          "ak-123",
		Name:        "my-key",
		Description: "Test key",
		KeyPrefix:   "nlak_test",
		Scopes:      []string{"compute:read", "compute:write"},
		ExpiresAt:   "2026-12-31T23:59:59Z",
		RateLimit:   100,
		Status:      "active",
		CreatedAt:   "2025-06-01T12:00:00Z",
	}

	model := APIKeyModel{
		Scopes: types.ListNull(types.StringType),
	}
	model.fromAPI(ctx, key, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "ak-123" {
		t.Errorf("expected ID ak-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-key" {
		t.Errorf("expected name my-key, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "Test key" {
		t.Errorf("expected description 'Test key', got %s", model.Description.ValueString())
	}
	if model.KeyPrefix.ValueString() != "nlak_test" {
		t.Errorf("expected key_prefix nlak_test, got %s", model.KeyPrefix.ValueString())
	}
	if model.ExpiresAt.ValueString() != "2026-12-31T23:59:59Z" {
		t.Errorf("expected expires_at, got %s", model.ExpiresAt.ValueString())
	}
	if model.RateLimit.ValueInt64() != 100 {
		t.Errorf("expected rate_limit 100, got %d", model.RateLimit.ValueInt64())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-06-01T12:00:00Z" {
		t.Errorf("expected created_at, got %s", model.CreatedAt.ValueString())
	}
}

func TestAPIKeyModelFromAPIMinimalFields(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	key := &apiAPIKey{
		ID:        "ak-min",
		Name:      "basic-key",
		KeyPrefix: "nlak_basic",
		Status:    "active",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	model := APIKeyModel{
		Description: types.StringNull(),
		Scopes:      types.ListNull(types.StringType),
		RateLimit:   types.Int64Null(),
	}
	model.fromAPI(ctx, key, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.Description.IsNull() {
		t.Error("expected description to be null")
	}
	if !model.ExpiresAt.IsNull() {
		t.Error("expected expires_at to be null")
	}
	if !model.RateLimit.IsNull() {
		t.Error("expected rate_limit to be null")
	}
	if !model.Scopes.IsNull() {
		t.Error("expected scopes to be null")
	}
}

// --- HTTP integration tests ---

func newTestClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return c
}

func meHandler(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"id":       "user-123",
		"tenantId": "tenant-456",
		"email":    "test@example.com",
	})
}

func TestAPIKeyCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/api-keys":
			var req apiCreateAPIKeyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name != "my-key" {
				t.Errorf("expected name my-key, got %s", req.Name)
			}
			if req.Description != "A test key" {
				t.Errorf("expected description 'A test key', got %s", req.Description)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:          "ak-new",
				Name:        req.Name,
				Description: req.Description,
				Key:         "nlak_secretvalue123456", // pragma: allowlist secret
				KeyPrefix:   "nlak_secr",
				Scopes:      req.Scopes,
				RateLimit:   req.RateLimit,
				Status:      "active",
				CreatedAt:   "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	apiReq := apiCreateAPIKeyRequest{
		Name:        "my-key",
		Description: "A test key",
		Scopes:      []string{"compute:read"},
		RateLimit:   50,
	}
	resp, err := c.Post(context.Background(), "/v1/api-keys", apiReq)
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	key, err := client.ParseResponse[apiAPIKey](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if key.ID != "ak-new" {
		t.Errorf("expected ID ak-new, got %s", key.ID)
	}
	if key.Key != "nlak_secretvalue123456" { // pragma: allowlist secret
		t.Error("expected key value in create response")
	}
	if key.KeyPrefix != "nlak_secr" {
		t.Errorf("expected key_prefix nlak_secr, got %s", key.KeyPrefix)
	}
	if key.Status != "active" {
		t.Errorf("expected status active, got %s", key.Status)
	}
}

func TestAPIKeyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/api-keys/ak-123":
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:          "ak-123",
				Name:        "my-key",
				Description: "Test key",
				KeyPrefix:   "nlak_test",
				Scopes:      []string{"compute:read"},
				RateLimit:   50,
				Status:      "active",
				CreatedAt:   "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	resp, err := c.Get(context.Background(), "/v1/api-keys/ak-123", nil)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	key, err := client.ParseResponse[apiAPIKey](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if key.ID != "ak-123" {
		t.Errorf("expected ID ak-123, got %s", key.ID)
	}
	if key.Name != "my-key" {
		t.Errorf("expected name my-key, got %s", key.Name)
	}
	// Key should not be present on reads.
	if key.Key != "" {
		t.Error("expected empty key on read")
	}
	if key.Status != "active" {
		t.Errorf("expected status active, got %s", key.Status)
	}
}

func TestAPIKeyReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/api-keys/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "NOT_FOUND",
					"message": "API key not found",
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Get(context.Background(), "/v1/api-keys/nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

func TestAPIKeyUpdate(t *testing.T) {
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/api-keys/ak-123":
			patched = true
			var req apiUpdateAPIKeyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name == nil || *req.Name != "updated-key" {
				t.Errorf("expected name updated-key, got %v", req.Name)
			}
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "nlak_test",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/api-keys/ak-123":
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "nlak_test",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	name := "updated-key"
	updateReq := apiUpdateAPIKeyRequest{Name: &name}
	_, err := c.Patch(context.Background(), "/v1/api-keys/ak-123", updateReq)
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}

	if !patched {
		t.Error("expected patch to be called")
	}
}

func TestAPIKeyDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/api-keys/ak-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Delete(context.Background(), "/v1/api-keys/ak-123")
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if !deleted {
		t.Error("expected delete to be called")
	}
}

func TestAPIKeyDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/api-keys/gone":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "NOT_FOUND",
					"message": "API key not found",
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Delete(context.Background(), "/v1/api-keys/gone")
	if err == nil {
		t.Fatal("expected error")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

// --- Resource method tests (tfsdk-level) ---

func apiKeySchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &apiKeyResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func apiKeyTFType(t *testing.T) tftypes.Type {
	t.Helper()
	s := apiKeySchema(t)
	return s.Type().TerraformType(context.Background())
}

func configuredAPIKeyResource(t *testing.T, serverURL string) *apiKeyResource {
	t.Helper()
	realClient := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	if err := realClient.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &apiKeyResource{client: realClient}
}

func apiKeyMeServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			meHandler(w, r)
			return
		}
		handler(w, r)
	}))
}

func TestAPIKeyResource_NewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
	if _, ok := r.(*apiKeyResource); !ok {
		t.Fatalf("expected *apiKeyResource, got %T", r)
	}
}

func TestAPIKeyResource_Metadata(t *testing.T) {
	r := &apiKeyResource{}
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := resource.MetadataResponse{}
	r.Metadata(context.Background(), req, &resp)

	if resp.TypeName != "frostmoln_api_key" {
		t.Errorf("expected type name frostmoln_api_key, got %s", resp.TypeName)
	}
}

func TestAPIKeyResource_Schema_Attributes(t *testing.T) {
	s := apiKeySchema(t)
	if s.Description == "" {
		t.Error("expected non-empty schema description")
	}
	for _, name := range []string{"id", "name", "description", "scopes", "expires_at", "rate_limit", "key", "key_prefix", "status", "created_at"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("expected attribute %s in schema", name)
		}
	}
}

func TestAPIKeyResource_Configure_NilProviderData(t *testing.T) {
	r := &apiKeyResource{}
	req := resource.ConfigureRequest{ProviderData: nil}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no errors with nil provider data, got %v", resp.Diagnostics.Errors())
	}
	if r.client != nil {
		t.Error("expected nil client")
	}
}

func TestAPIKeyResource_Configure_ValidClient(t *testing.T) {
	r := &apiKeyResource{}
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	req := resource.ConfigureRequest{ProviderData: c}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

func TestAPIKeyResource_Configure_WrongType(t *testing.T) {
	r := &apiKeyResource{}
	req := resource.ConfigureRequest{ProviderData: "wrong"}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for wrong type")
	}
}

func TestAPIKeyResource_Create_TFSDK(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/api-keys":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-new",
				Name:      "test-key",
				Key:       "nlak_secret123", // pragma: allowlist secret
				KeyPrefix: "nlak_secr",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"key_prefix":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Create(ctx, createReq, createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", createResp.Diagnostics.Errors())
	}

	var model APIKeyModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "ak-new" {
		t.Errorf("expected ID ak-new, got %s", model.ID.ValueString())
	}
	if model.Key.ValueString() != "nlak_secret123" { // pragma: allowlist secret
		t.Errorf("expected key nlak_secret123, got %s", model.Key.ValueString())
	}
	if model.KeyPrefix.ValueString() != "nlak_secr" {
		t.Errorf("expected key_prefix nlak_secr, got %s", model.KeyPrefix.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
}

func TestAPIKeyResource_Create_APIError(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"key_prefix":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Create(ctx, createReq, createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestAPIKeyResource_Read_TFSDK(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/api-keys/ak-123":
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "my-key",
				KeyPrefix: "nlak_test",
				Scopes:    []string{"compute:read"},
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "my-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{tftypes.NewValue(tftypes.String, "compute:read")}),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak_savedkey"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak_test"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", readResp.Diagnostics.Errors())
	}

	var model APIKeyModel
	readResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "ak-123" {
		t.Errorf("expected ID ak-123, got %s", model.ID.ValueString())
	}
	// Key should be preserved from state since API does not return it.
	if model.Key.ValueString() != "nlak_savedkey" { // pragma: allowlist secret
		t.Errorf("expected key to be preserved from state, got %s", model.Key.ValueString())
	}
	if model.KeyPrefix.ValueString() != "nlak_test" {
		t.Errorf("expected key_prefix nlak_test, got %s", model.KeyPrefix.ValueString())
	}
}

func TestAPIKeyResource_Read_NotFound_TFSDK(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "nonexistent"),
		"name":        tftypes.NewValue(tftypes.String, "key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors on not-found, got %v", readResp.Diagnostics.Errors())
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("expected null state after not-found read")
	}
}

func TestAPIKeyResource_Update_TFSDK(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/api-keys/ak-123":
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "nlak_test",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/api-keys/ak-123":
			json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "nlak_test",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	// State (current)
	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "old-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak_test"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan (desired)
	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "updated-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak_test"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	updateResp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Update(ctx, updateReq, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", updateResp.Diagnostics.Errors())
	}

	var model APIKeyModel
	updateResp.State.Get(ctx, &model)
	if model.Name.ValueString() != "updated-key" {
		t.Errorf("expected name updated-key, got %s", model.Name.ValueString())
	}
	// Key should be preserved from state.
	if model.Key.ValueString() != "nlak_saved" { // pragma: allowlist secret
		t.Errorf("expected key to be preserved, got %s", model.Key.ValueString())
	}
}

func TestAPIKeyResource_Update_APIError(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "old-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak_test"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "updated-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak_test"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	updateResp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Update(ctx, updateReq, updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestAPIKeyResource_Delete_TFSDK(t *testing.T) {
	deleted := false
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/api-keys/ak-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "my-key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak_key"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected delete API call")
	}
}

func TestAPIKeyResource_Delete_NotFound_TFSDK(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredAPIKeyResource(t, server.URL)
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "gone"),
		"name":        tftypes.NewValue(tftypes.String, "key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "nlak"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "nlak"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	// Deleting a resource that is already gone should succeed silently.
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors on delete of nonexistent resource, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestAPIKeyResource_ImportState_TFSDK(t *testing.T) {
	r := &apiKeyResource{}
	s := apiKeySchema(t)
	tfType := apiKeyTFType(t)

	// Initialize state with null values so the schema type is set.
	initVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, nil),
		"key_prefix":  tftypes.NewValue(tftypes.String, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	importReq := resource.ImportStateRequest{ID: "ak-123"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: initVal},
	}

	r.ImportState(context.Background(), importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", importResp.Diagnostics.Errors())
	}

	var model APIKeyModel
	importResp.State.Get(context.Background(), &model)
	if model.ID.ValueString() != "ak-123" {
		t.Errorf("expected imported ID ak-123, got %s", model.ID.ValueString())
	}
}
