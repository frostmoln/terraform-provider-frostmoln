package api_key

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

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
