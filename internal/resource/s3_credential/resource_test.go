package s3_credential

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestS3CredentialModelToCreateRequest(t *testing.T) {
	model := S3CredentialModel{
		Name:        types.StringValue("my-cred"),
		Description: types.StringValue("test description"),
	}

	req, _ := model.toCreateRequest(context.Background())

	if req.Name != "my-cred" {
		t.Errorf("expected name my-cred, got %s", req.Name)
	}
	if req.Description != "test description" {
		t.Errorf("expected description 'test description', got %s", req.Description)
	}
}

func TestS3CredentialModelToCreateRequestNoDescription(t *testing.T) {
	model := S3CredentialModel{
		Name:        types.StringValue("my-cred"),
		Description: types.StringNull(),
	}

	req, _ := model.toCreateRequest(context.Background())

	if req.Name != "my-cred" {
		t.Errorf("expected name my-cred, got %s", req.Name)
	}
	if req.Description != "" {
		t.Errorf("expected empty description, got %s", req.Description)
	}
}

func TestS3CredentialModelFromAPI(t *testing.T) {
	cred := &apiS3Credential{
		ID:              "cred-123",
		Name:            "my-cred",
		Description:     "test description",
		SecretAccessKey: "super-secret-key", // pragma: allowlist secret
		Status:          "active",
		CreatedAt:       "2025-06-01T12:00:00Z",
	}

	var model S3CredentialModel
	model.fromAPI(context.Background(), cred)

	if model.ID.ValueString() != "cred-123" {
		t.Errorf("expected ID cred-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-cred" {
		t.Errorf("expected name my-cred, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "test description" {
		t.Errorf("expected description 'test description', got %s", model.Description.ValueString())
	}
	if model.SecretAccessKey.ValueString() != "super-secret-key" { // pragma: allowlist secret
		t.Errorf("expected secret access key, got %s", model.SecretAccessKey.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-06-01T12:00:00Z" {
		t.Errorf("expected created_at, got %s", model.CreatedAt.ValueString())
	}
}

func TestS3CredentialModelFromAPINoSecret(t *testing.T) {
	cred := &apiS3Credential{
		ID:        "cred-123",
		Name:      "my-cred",
		Status:    "active",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	// Simulate existing state with a secret key.
	model := S3CredentialModel{
		SecretAccessKey: types.StringValue("existing-secret"), // pragma: allowlist secret
	}
	model.fromAPI(context.Background(), cred)

	// fromAPI should not overwrite the secret_access_key when the API returns empty.
	if model.SecretAccessKey.ValueString() != "existing-secret" { // pragma: allowlist secret
		t.Errorf("expected secret to be preserved, got %s", model.SecretAccessKey.ValueString())
	}
}

func TestS3CredentialModelFromAPIEmptyDescription(t *testing.T) {
	cred := &apiS3Credential{
		ID:        "cred-123",
		Name:      "my-cred",
		Status:    "active",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	model := S3CredentialModel{
		Description: types.StringNull(),
	}
	model.fromAPI(context.Background(), cred)

	if !model.Description.IsNull() {
		t.Errorf("expected null description, got %s", model.Description.ValueString())
	}
}

func TestS3CredentialCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/credentials":
			var req apiCreateS3CredentialRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name != "test-cred" {
				t.Errorf("expected name test-cred, got %s", req.Name)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiS3Credential{
				ID:              "cred-abc",
				Name:            req.Name,
				Description:     req.Description,
				SecretAccessKey: "generated-secret-key", // pragma: allowlist secret
				Status:          "active",
				CreatedAt:       "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	apiReq := apiCreateS3CredentialRequest{Name: "test-cred", Description: "test"}
	resp, err := c.Post(context.Background(), c.TenantPath("/credentials"), apiReq)
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	cred, err := client.ParseResponse[apiS3Credential](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if cred.ID != "cred-abc" {
		t.Errorf("expected ID cred-abc, got %s", cred.ID)
	}
	if cred.SecretAccessKey != "generated-secret-key" { // pragma: allowlist secret
		t.Errorf("expected secret access key, got %s", cred.SecretAccessKey)
	}
	if cred.Status != "active" {
		t.Errorf("expected status active, got %s", cred.Status)
	}
}

func TestS3CredentialReadFromList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/credentials":
			json.NewEncoder(w).Encode(apiS3CredentialList{
				Credentials: []apiS3Credential{
					{
						ID:        "cred-other",
						Name:      "other-cred",
						Status:    "active",
						CreatedAt: "2025-05-01T10:00:00Z",
					},
					{
						ID:        "cred-abc",
						Name:      "test-cred",
						Status:    "active",
						CreatedAt: "2025-06-01T12:00:00Z",
					},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	resp, err := c.Get(context.Background(), c.TenantPath("/credentials"), nil)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	list, err := client.ParseResponse[apiS3CredentialList](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	// Verify we can find the credential by ID.
	var found *apiS3Credential
	for i := range list.Credentials {
		if list.Credentials[i].ID == "cred-abc" {
			found = &list.Credentials[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected to find credential cred-abc in list")
	}
	if found.Name != "test-cred" {
		t.Errorf("expected name test-cred, got %s", found.Name)
	}
}

func TestS3CredentialReadNotFoundInList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/credentials":
			json.NewEncoder(w).Encode(apiS3CredentialList{
				Credentials: []apiS3Credential{
					{
						ID:        "cred-other",
						Name:      "other-cred",
						Status:    "active",
						CreatedAt: "2025-05-01T10:00:00Z",
					},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	resp, err := c.Get(context.Background(), c.TenantPath("/credentials"), nil)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	list, err := client.ParseResponse[apiS3CredentialList](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	var found *apiS3Credential
	for i := range list.Credentials {
		if list.Credentials[i].ID == "cred-nonexistent" {
			found = &list.Credentials[i]
			break
		}
	}

	if found != nil {
		t.Error("expected credential to not be found")
	}
}

func TestS3CredentialDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/credentials/cred-abc":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Delete(context.Background(), c.TenantPath("/credentials/cred-abc"))
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if !deleted {
		t.Error("expected delete to be called")
	}
}

// --- Resource method tests (tfsdk-level) ---

func s3CredMeServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
			return
		}
		handler(w, r)
	}))
}

func configuredS3CredResource(t *testing.T, serverURL string) *s3CredentialResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &s3CredentialResource{client: c}
}

func s3CredSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &s3CredentialResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func s3CredTFType(t *testing.T) tftypes.Type {
	t.Helper()
	s := s3CredSchema(t)
	return s.Type().TerraformType(context.Background())
}

func TestS3CredentialResource_NewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
	if _, ok := r.(*s3CredentialResource); !ok {
		t.Fatalf("expected *s3CredentialResource, got %T", r)
	}
}

func TestS3CredentialResource_Metadata(t *testing.T) {
	r := &s3CredentialResource{}
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := resource.MetadataResponse{}
	r.Metadata(context.Background(), req, &resp)

	if resp.TypeName != "frostmoln_s3_credential" {
		t.Errorf("expected type name frostmoln_s3_credential, got %s", resp.TypeName)
	}
}

func TestS3CredentialResource_Schema_Attributes(t *testing.T) {
	s := s3CredSchema(t)
	if s.Description == "" {
		t.Error("expected non-empty schema description")
	}
	for _, name := range []string{"id", "name", "description", "secret_access_key", "status", "created_at", "allowed_buckets", "allowed_actions", "ip_whitelist"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("expected attribute %s in schema", name)
		}
	}
}

func TestS3CredentialModel_ScopingRoundTrip(t *testing.T) {
	ctx := context.Background()
	buckets, _ := types.ListValueFrom(ctx, types.StringType, []string{"b1", "b2"})
	actions, _ := types.ListValueFrom(ctx, types.StringType, []string{"s3:GetObject"})
	ips, _ := types.ListValueFrom(ctx, types.StringType, []string{"203.0.113.0/24"})
	model := S3CredentialModel{
		Name:           types.StringValue("scoped"),
		AllowedBuckets: buckets,
		AllowedActions: actions,
		IPWhitelist:    ips,
	}

	req, diags := model.toCreateRequest(ctx)
	if diags.HasError() {
		t.Fatalf("toCreateRequest diags: %v", diags.Errors())
	}
	if len(req.AllowedBuckets) != 2 || req.AllowedBuckets[0] != "b1" {
		t.Errorf("expected allowed_buckets [b1 b2], got %v", req.AllowedBuckets)
	}
	if len(req.AllowedActions) != 1 || req.AllowedActions[0] != "s3:GetObject" {
		t.Errorf("expected allowed_actions [s3:GetObject], got %v", req.AllowedActions)
	}
	if len(req.IPWhitelist) != 1 || req.IPWhitelist[0] != "203.0.113.0/24" {
		t.Errorf("expected ip_whitelist [203.0.113.0/24], got %v", req.IPWhitelist)
	}

	// Read-back: the API echoes the scope; the model must reflect it.
	var got S3CredentialModel
	if d := got.fromAPI(ctx, &apiS3Credential{
		ID: "c1", Name: "scoped", Status: "active", CreatedAt: "t",
		AllowedBuckets: []string{"b1", "b2"},
		AllowedActions: []string{"s3:GetObject"},
		IPWhitelist:    []string{"203.0.113.0/24"},
	}); d.HasError() {
		t.Fatalf("fromAPI diags: %v", d.Errors())
	}
	var rb []string
	got.AllowedBuckets.ElementsAs(ctx, &rb, false)
	if len(rb) != 2 || rb[1] != "b2" {
		t.Errorf("expected read-back allowed_buckets [b1 b2], got %v", rb)
	}
}

func TestS3CredentialModel_UnscopedIsNull(t *testing.T) {
	ctx := context.Background()
	// Unset scoping => omitted from request, and read-back of an empty API
	// response keeps the fields null (no perpetual null-vs-[] drift).
	model := S3CredentialModel{Name: types.StringValue("open")}
	req, diags := model.toCreateRequest(ctx)
	if diags.HasError() {
		t.Fatalf("toCreateRequest diags: %v", diags.Errors())
	}
	if req.AllowedBuckets != nil || req.AllowedActions != nil || req.IPWhitelist != nil {
		t.Errorf("expected nil scope slices, got %v / %v / %v", req.AllowedBuckets, req.AllowedActions, req.IPWhitelist)
	}
	// The unset scope MUST be omitted from the wire JSON (omitempty on nil
	// slices) — otherwise the backend could misread "unrestricted" intent.
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, k := range []string{"allowedBuckets", "allowedActions", "ipWhitelist"} {
		if bytes.Contains(wire, []byte(k)) {
			t.Errorf("unset scope must be omitted from wire JSON, found %q in %s", k, wire)
		}
	}

	var got S3CredentialModel
	if d := got.fromAPI(ctx, &apiS3Credential{ID: "c1", Name: "open", Status: "active", CreatedAt: "t"}); d.HasError() {
		t.Fatalf("fromAPI diags: %v", d.Errors())
	}
	if !got.AllowedBuckets.IsNull() || !got.AllowedActions.IsNull() || !got.IPWhitelist.IsNull() {
		t.Error("expected unset scope lists to be null after read-back")
	}
}

// TestS3CredentialModel_EmptyReadDoesNotWidenScope is the anti-scope-widening
// invariant: if state holds a restriction but the API returns an empty scope
// (transient/partial read), fromAPI MUST preserve the existing restriction —
// never silently clear it (which a later apply would push as a scope widening).
func TestS3CredentialModel_EmptyReadDoesNotWidenScope(t *testing.T) {
	ctx := context.Background()
	existing, _ := types.ListValueFrom(ctx, types.StringType, []string{"b1"})
	model := S3CredentialModel{
		AllowedBuckets: existing,
		AllowedActions: existing,
		IPWhitelist:    existing,
	}

	// API echoes nothing for the scope axes.
	if d := model.fromAPI(ctx, &apiS3Credential{ID: "c1", Name: "scoped", Status: "active", CreatedAt: "t"}); d.HasError() {
		t.Fatalf("fromAPI diags: %v", d.Errors())
	}

	for name, l := range map[string]types.List{"allowed_buckets": model.AllowedBuckets, "allowed_actions": model.AllowedActions, "ip_whitelist": model.IPWhitelist} {
		if l.IsNull() {
			t.Errorf("%s was cleared by an empty read (scope widening)", name)
			continue
		}
		var vals []string
		l.ElementsAs(ctx, &vals, false)
		if len(vals) != 1 || vals[0] != "b1" {
			t.Errorf("%s restriction not preserved, got %v", name, vals)
		}
	}
}

func TestS3CredentialResource_Configure_NilProviderData(t *testing.T) {
	r := &s3CredentialResource{}
	req := resource.ConfigureRequest{ProviderData: nil}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
	if r.client != nil {
		t.Error("expected nil client")
	}
}

func TestS3CredentialResource_Configure_ValidClient(t *testing.T) {
	r := &s3CredentialResource{}
	c := client.NewClient("http://localhost", "test-key")
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

func TestS3CredentialResource_Configure_WrongType(t *testing.T) {
	r := &s3CredentialResource{}
	req := resource.ConfigureRequest{ProviderData: 42}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for wrong type")
	}
}

func TestS3CredentialResource_Create_TFSDK(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/credentials":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiS3Credential{
				ID:              "cred-abc",
				Name:            "test-cred",
				Description:     "test",
				SecretAccessKey: "generated-secret", // pragma: allowlist secret
				Status:          "active",
				CreatedAt:       "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":              tftypes.NewValue(tftypes.String, "test-cred"),
		"description":       tftypes.NewValue(tftypes.String, "test"),
		"secret_access_key": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"status":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
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

	var model S3CredentialModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "cred-abc" {
		t.Errorf("expected ID cred-abc, got %s", model.ID.ValueString())
	}
	if model.SecretAccessKey.ValueString() != "generated-secret" { // pragma: allowlist secret
		t.Errorf("expected secret access key, got %s", model.SecretAccessKey.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
}

func TestS3CredentialResource_Create_APIError(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":              tftypes.NewValue(tftypes.String, "test-cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"status":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
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

func TestS3CredentialResource_Read_TFSDK(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/credentials":
			json.NewEncoder(w).Encode(apiS3CredentialList{
				Credentials: []apiS3Credential{
					{
						ID:        "cred-other",
						Name:      "other-cred",
						Status:    "active",
						CreatedAt: "2025-05-01T10:00:00Z",
					},
					{
						ID:        "cred-abc",
						Name:      "test-cred",
						Status:    "active",
						CreatedAt: "2025-06-01T12:00:00Z",
					},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "cred-abc"),
		"name":              tftypes.NewValue(tftypes.String, "test-cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, "saved-secret"), // pragma: allowlist secret
		"status":            tftypes.NewValue(tftypes.String, "active"),
		"created_at":        tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
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

	var model S3CredentialModel
	readResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "cred-abc" {
		t.Errorf("expected ID cred-abc, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-cred" {
		t.Errorf("expected name test-cred, got %s", model.Name.ValueString())
	}
	// Secret should be preserved from state since the API doesn't return it on read.
	if model.SecretAccessKey.ValueString() != "saved-secret" { // pragma: allowlist secret
		t.Errorf("expected secret to be preserved from state, got %s", model.SecretAccessKey.ValueString())
	}
}

func TestS3CredentialResource_Read_NotFoundInList_TFSDK(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/credentials":
			json.NewEncoder(w).Encode(apiS3CredentialList{
				Credentials: []apiS3Credential{
					{ID: "cred-other", Name: "other", Status: "active", CreatedAt: "2025-05-01T10:00:00Z"},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "cred-nonexistent"),
		"name":              tftypes.NewValue(tftypes.String, "gone-cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, "old-secret"), // pragma: allowlist secret
		"status":            tftypes.NewValue(tftypes.String, "active"),
		"created_at":        tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors, got %v", readResp.Diagnostics.Errors())
	}
	// State should be removed since the credential was not found in the list.
	if !readResp.State.Raw.IsNull() {
		t.Error("expected null state after credential not found in list")
	}
}

func TestS3CredentialResource_Read_APIError(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "cred-abc"),
		"name":              tftypes.NewValue(tftypes.String, "test-cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, "secret"), // pragma: allowlist secret
		"status":            tftypes.NewValue(tftypes.String, "active"),
		"created_at":        tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestS3CredentialResource_Update_TFSDK(t *testing.T) {
	r := &s3CredentialResource{}
	resp := &resource.UpdateResponse{}
	r.Update(context.Background(), resource.UpdateRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error from unsupported update")
	}

	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if d.Summary() == "Update Not Supported" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Update Not Supported' error")
	}
}

func TestS3CredentialResource_Delete_TFSDK(t *testing.T) {
	deleted := false
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/credentials/cred-abc":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "cred-abc"),
		"name":              tftypes.NewValue(tftypes.String, "test-cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, "secret"), // pragma: allowlist secret
		"status":            tftypes.NewValue(tftypes.String, "active"),
		"created_at":        tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
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

func TestS3CredentialResource_Delete_NotFound_TFSDK(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "gone"),
		"name":              tftypes.NewValue(tftypes.String, "cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, "secret"), // pragma: allowlist secret
		"status":            tftypes.NewValue(tftypes.String, "active"),
		"created_at":        tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors on delete of nonexistent resource, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestS3CredentialResource_Delete_APIError(t *testing.T) {
	server := s3CredMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredS3CredResource(t, server.URL)
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "cred-abc"),
		"name":              tftypes.NewValue(tftypes.String, "test-cred"),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, "secret"), // pragma: allowlist secret
		"status":            tftypes.NewValue(tftypes.String, "active"),
		"created_at":        tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestS3CredentialResource_ImportState_TFSDK(t *testing.T) {
	r := &s3CredentialResource{}
	s := s3CredSchema(t)
	tfType := s3CredTFType(t)

	// Initialize state with null values so the schema type is set.
	initVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, nil),
		"name":              tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
		"secret_access_key": tftypes.NewValue(tftypes.String, nil),
		"status":            tftypes.NewValue(tftypes.String, nil),
		"created_at":        tftypes.NewValue(tftypes.String, nil),
		"allowed_buckets":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"allowed_actions":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"ip_whitelist":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	})

	importReq := resource.ImportStateRequest{ID: "cred-abc"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: initVal},
	}

	r.ImportState(context.Background(), importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", importResp.Diagnostics.Errors())
	}

	var model S3CredentialModel
	importResp.State.Get(context.Background(), &model)
	if model.ID.ValueString() != "cred-abc" {
		t.Errorf("expected imported ID cred-abc, got %s", model.ID.ValueString())
	}
}
