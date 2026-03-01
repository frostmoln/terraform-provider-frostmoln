package s3_credential

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

func TestS3CredentialModelToCreateRequest(t *testing.T) {
	model := S3CredentialModel{
		Name:        types.StringValue("my-cred"),
		Description: types.StringValue("test description"),
	}

	req := model.toCreateRequest()

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

	req := model.toCreateRequest()

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
	model.fromAPI(cred)

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
	model.fromAPI(cred)

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
	model.fromAPI(cred)

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
