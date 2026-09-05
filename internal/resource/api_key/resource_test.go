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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
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
		KeyPrefix:   "fmk_test",
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
	if model.KeyPrefix.ValueString() != "fmk_test" {
		t.Errorf("expected key_prefix fmk_test, got %s", model.KeyPrefix.ValueString())
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
		KeyPrefix: "fmk_basic",
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

func TestNormalizeAPIKeyExpiry(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty stays empty (server default)", in: "", want: ""},
		{name: "bare date -> end of day UTC", in: "2027-01-01", want: "2027-01-01T23:59:59Z"},
		{name: "RFC3339 UTC round-trips", in: "2027-01-01T10:00:00Z", want: "2027-01-01T10:00:00Z"},
		{name: "RFC3339 offset canonicalized to UTC", in: "2027-01-01T12:00:00+02:00", want: "2027-01-01T10:00:00Z"},
		{name: "garbage errors", in: "not-a-date", wantErr: true},
		{name: "impossible date errors", in: "2027-13-99", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeAPIKeyExpiry(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("normalizeAPIKeyExpiry(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAPIKeyModelToCreateRequestBareDate(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := APIKeyModel{
		Name:        types.StringValue("dated-key"),
		Description: types.StringNull(),
		Scopes:      types.ListNull(types.StringType),
		ExpiresAt:   types.StringValue("2027-01-01"), // bare date, the bug repro
		RateLimit:   types.Int64Null(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	// The wire value must be the canonical RFC3339 identity accepts, not the
	// bare date that caused the 400.
	if req.ExpiresAt != "2027-01-01T23:59:59Z" {
		t.Errorf("expected wire expires_at 2027-01-01T23:59:59Z, got %s", req.ExpiresAt)
	}
}

func TestAPIKeyModelToCreateRequestBadDate(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := APIKeyModel{
		Name:      types.StringValue("bad-key"),
		Scopes:    types.ListNull(types.StringType),
		ExpiresAt: types.StringValue("whenever"),
		RateLimit: types.Int64Null(),
	}

	model.toCreateRequest(ctx, &diags)
	if !diags.HasError() {
		t.Fatal("expected a diagnostic error for an unparseable expires_at")
	}
}

func TestAPIKeyModelFromAPIPreservesConfigExpiry(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	// Model already holds the user's bare date (what config said); the API
	// returns the normalized RFC3339 for the same instant.
	model := APIKeyModel{
		Scopes:    types.ListNull(types.StringType),
		ExpiresAt: types.StringValue("2027-01-01"),
	}
	key := &apiAPIKey{
		ID:        "ak-1",
		Name:      "k",
		KeyPrefix: "fmk",
		Status:    "active",
		CreatedAt: "2025-06-01T12:00:00Z",
		ExpiresAt: "2027-01-01T23:59:59Z",
	}

	model.fromAPI(ctx, key, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	// state must equal config, else "provider produced inconsistent result".
	if model.ExpiresAt.ValueString() != "2027-01-01" {
		t.Errorf("expected config string preserved (2027-01-01), got %s", model.ExpiresAt.ValueString())
	}
}

func TestAPIKeyModelFromAPIDifferentInstantTakesAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	// Config instant and API instant differ -> the API value wins (e.g. after
	// an out-of-band change or an import overwriting a stale value).
	model := APIKeyModel{
		Scopes:    types.ListNull(types.StringType),
		ExpiresAt: types.StringValue("2027-01-01"),
	}
	key := &apiAPIKey{
		ID:        "ak-2",
		Name:      "k",
		KeyPrefix: "fmk",
		Status:    "active",
		CreatedAt: "2025-06-01T12:00:00Z",
		ExpiresAt: "2028-06-06T00:00:00Z",
	}

	model.fromAPI(ctx, key, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if model.ExpiresAt.ValueString() != "2028-06-06T00:00:00Z" {
		t.Errorf("expected API value, got %s", model.ExpiresAt.ValueString())
	}
}

func TestExpiresAtSemanticEquality(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		state      types.String
		config     types.String
		wantPlan   string // expected plan value after the modifier
		wantIsNull bool
	}{
		{
			name:     "bare date equals canonical state -> keep state (no diff)",
			state:    types.StringValue("2027-01-01T23:59:59Z"),
			config:   types.StringValue("2027-01-01"),
			wantPlan: "2027-01-01T23:59:59Z",
		},
		{
			name:     "different instant -> keep config (forces replace downstream)",
			state:    types.StringValue("2027-01-01T23:59:59Z"),
			config:   types.StringValue("2028-01-01"),
			wantPlan: "2028-01-01",
		},
		{
			name:       "null state (create) -> untouched",
			state:      types.StringNull(),
			config:     types.StringValue("2027-01-01"),
			wantIsNull: false,
			wantPlan:   "2027-01-01",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := planmodifier.StringRequest{
				StateValue:  tc.state,
				ConfigValue: tc.config,
				PlanValue:   tc.config, // framework seeds plan from config for a known value
			}
			resp := &planmodifier.StringResponse{PlanValue: tc.config}
			expiresAtSemanticEquality{}.PlanModifyString(ctx, req, resp)
			if resp.PlanValue.ValueString() != tc.wantPlan {
				t.Errorf("plan value = %q, want %q", resp.PlanValue.ValueString(), tc.wantPlan)
			}
		})
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
	_ = json.NewEncoder(w).Encode(map[string]string{
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

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/api-keys":
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
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:          "ak-new",
				Name:        req.Name,
				Description: req.Description,
				Key:         "fmk_secretvalue123456", // pragma: allowlist secret
				KeyPrefix:   "fmk_secr",
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
	resp, err := c.Post(context.Background(), "/v1/tenants/tenant-456/api-keys", apiReq)
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
	if key.Key != "fmk_secretvalue123456" { // pragma: allowlist secret
		t.Error("expected key value in create response")
	}
	if key.KeyPrefix != "fmk_secr" {
		t.Errorf("expected key_prefix fmk_secr, got %s", key.KeyPrefix)
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

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:          "ak-123",
				Name:        "my-key",
				Description: "Test key",
				KeyPrefix:   "fmk_test",
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

	resp, err := c.Get(context.Background(), "/v1/tenants/tenant-456/api-keys/ak-123", nil)
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

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/api-keys/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code":    "NOT_FOUND",
				"message": "API key not found",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Get(context.Background(), "/v1/tenants/tenant-456/api-keys/nonexistent", nil)
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

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			patched = true
			var req apiUpdateAPIKeyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name == nil || *req.Name != "updated-key" {
				t.Errorf("expected name updated-key, got %v", req.Name)
			}
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "fmk_test",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "fmk_test",
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
	_, err := c.Patch(context.Background(), "/v1/tenants/tenant-456/api-keys/ak-123", updateReq)
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

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Delete(context.Background(), "/v1/tenants/tenant-456/api-keys/ak-123")
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

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/api-keys/gone":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code":    "NOT_FOUND",
				"message": "API key not found",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Delete(context.Background(), "/v1/tenants/tenant-456/api-keys/gone")
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/api-keys":
			// Decode the request and echo it back inside the create ENVELOPE
			// ({apiKey: {...}, key: "..."}) exactly as identity does, so the
			// test exercises both the envelope decode and the expires_at
			// normalize/readback-preserve round-trip.
			var req apiCreateAPIKeyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateAPIKeyResponse{
				APIKey: apiAPIKey{
					ID:        "ak-new",
					Name:      req.Name,
					Scopes:    req.Scopes,
					ExpiresAt: req.ExpiresAt, // server echoes the RFC3339 it received
					KeyPrefix: "fmk_secr",
					Status:    "active",
					CreatedAt: "2025-06-01T12:00:00Z",
				},
				Key: "fmk_secret123", // pragma: allowlist secret
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
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{tftypes.NewValue(tftypes.String, "compute:read")}),
		"expires_at":  tftypes.NewValue(tftypes.String, "2027-01-01"),
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
	if model.Key.ValueString() != "fmk_secret123" { // pragma: allowlist secret
		t.Errorf("expected key fmk_secret123, got %s", model.Key.ValueString())
	}
	if model.KeyPrefix.ValueString() != "fmk_secr" {
		t.Errorf("expected key_prefix fmk_secr, got %s", model.KeyPrefix.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
	// Nested envelope fields must survive (they were empty before the fix).
	if model.Name.ValueString() != "test-key" {
		t.Errorf("expected name test-key, got %q", model.Name.ValueString())
	}
	var scopes []string
	model.Scopes.ElementsAs(ctx, &scopes, false)
	if len(scopes) != 1 || scopes[0] != "compute:read" {
		t.Errorf("expected scopes [compute:read], got %v", scopes)
	}
	// expires_at: the bare date the user wrote is preserved (state == config)
	// even though the wire value was the normalized RFC3339.
	if model.ExpiresAt.ValueString() != "2027-01-01" {
		t.Errorf("expected expires_at preserved as 2027-01-01, got %q", model.ExpiresAt.ValueString())
	}
}

func TestAPIKeyResource_Create_APIError(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "my-key",
				KeyPrefix: "fmk_test",
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
		"key":         tftypes.NewValue(tftypes.String, "fmk_savedkey"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk_test"),
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
	if model.Key.ValueString() != "fmk_savedkey" { // pragma: allowlist secret
		t.Errorf("expected key to be preserved from state, got %s", model.Key.ValueString())
	}
	if model.KeyPrefix.ValueString() != "fmk_test" {
		t.Errorf("expected key_prefix fmk_test, got %s", model.KeyPrefix.ValueString())
	}
}

func TestAPIKeyResource_Read_NotFound_TFSDK(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
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
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk"),
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
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "fmk_test",
				Status:    "active",
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
			_ = json.NewEncoder(w).Encode(apiAPIKey{
				ID:        "ak-123",
				Name:      "updated-key",
				KeyPrefix: "fmk_test",
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
		"key":         tftypes.NewValue(tftypes.String, "fmk_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk_test"),
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
		"key":         tftypes.NewValue(tftypes.String, "fmk_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk_test"),
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
	if model.Key.ValueString() != "fmk_saved" { // pragma: allowlist secret
		t.Errorf("expected key to be preserved, got %s", model.Key.ValueString())
	}
}

func TestAPIKeyResource_Update_APIError(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
		"key":         tftypes.NewValue(tftypes.String, "fmk_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk_test"),
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
		"key":         tftypes.NewValue(tftypes.String, "fmk_saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk_test"),
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
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/api-keys/ak-123":
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
		"key":         tftypes.NewValue(tftypes.String, "fmk_key"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk"),
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
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
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
		"key":         tftypes.NewValue(tftypes.String, "fmk"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk"),
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

// TestAPIKeyPathRefusesTraversal is the regression test for a DESTROYED
// resource, not a tidy-up.
//
// `id` is Computed, so a practitioner reaches it through `terraform import` or a
// supplied/edited state file — a shared module, a CI pipeline that builds an
// import block, tampered remote state. Client.do finishes URL assembly with
// path.Join, which CLEANS dot segments, and TenantPath escapes only the tenant;
// url.PathEscape would not help either, since "." and ".." are unreserved. So an
// unguarded id of "../instances/<uuid>" turns the DELETE that Terraform issues on
// destroy into a delete of that INSTANCE — a registered route, same verb,
// authorized by the practitioner's own credential — while the plan says "api
// key" throughout. Three more dot segments reach /api/v1/me, identity's
// self-service account delete.
//
// Moving these routes under /v1/tenants/{tid}/ only added segments to climb. The
// guard is what closes it, so this asserts the guard, and asserts a valid id is
// still built correctly so the guard cannot be "fixed" by refusing everything.
func TestAPIKeyPathRefusesTraversal(t *testing.T) {
	c := client.NewClient("https://api.example.test", "test-key")
	c.SetTenantIDForTest("tenant-456")
	r := &apiKeyResource{client: c}

	for _, id := range []string{
		"..",
		".",
		"",
		"../instances/9f2c",
		"../../v1/me",
		"a/b",
	} {
		if _, err := r.apiKeyPath(id, ""); err == nil {
			t.Errorf("apiKeyPath(%q) was accepted; it collapses onto another resource", id)
		}
		if _, err := r.apiKeyPath(id, "/revoke"); err == nil {
			t.Errorf("apiKeyPath(%q, /revoke) was accepted", id)
		}
	}

	got, err := r.apiKeyPath("ak-123", "")
	if err != nil {
		t.Fatalf("a valid id must still build a path: %v", err)
	}
	if want := "/v1/tenants/tenant-456/api-keys/ak-123"; got != want {
		t.Errorf("apiKeyPath = %q, want %q", got, want)
	}
}
