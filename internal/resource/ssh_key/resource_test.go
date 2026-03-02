package ssh_key

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

func TestSSHKeyModelToCreateRequest(t *testing.T) {
	model := SSHKeyModel{}
	model.Name = typesStringValue("my-key")
	model.PublicKey = typesStringValue("ssh-ed25519 AAAA... user@host")

	req := model.toCreateRequest()

	if req.Name != "my-key" {
		t.Errorf("expected name my-key, got %s", req.Name)
	}
	if req.PublicKey != "ssh-ed25519 AAAA... user@host" {
		t.Errorf("expected public key ssh-ed25519 AAAA... user@host, got %s", req.PublicKey)
	}
}

func TestSSHKeyModelFromAPI(t *testing.T) {
	key := &apiSSHKey{
		ID:          "key-123",
		Name:        "my-key",
		PublicKey:   "ssh-ed25519 AAAA... user@host",
		Fingerprint: "SHA256:abc123",
		CreatedAt:   "2025-01-15T10:00:00Z",
	}

	var model SSHKeyModel
	model.fromAPI(key)

	if model.ID.ValueString() != "key-123" {
		t.Errorf("expected ID key-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-key" {
		t.Errorf("expected name my-key, got %s", model.Name.ValueString())
	}
	if model.PublicKey.ValueString() != "ssh-ed25519 AAAA... user@host" {
		t.Errorf("expected public key, got %s", model.PublicKey.ValueString())
	}
	if model.Fingerprint.ValueString() != "SHA256:abc123" {
		t.Errorf("expected fingerprint SHA256:abc123, got %s", model.Fingerprint.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-01-15T10:00:00Z" {
		t.Errorf("expected created_at 2025-01-15T10:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestSSHKeyCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
				"email":    "test@example.com",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/sshkeys":
			var req apiCreateSSHKeyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name != "test-key" {
				t.Errorf("expected name test-key, got %s", req.Name)
			}
			if req.PublicKey != "ssh-ed25519 AAAA..." {
				t.Errorf("expected public key ssh-ed25519 AAAA..., got %s", req.PublicKey)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiSSHKey{
				ID:          "key-abc",
				Name:        req.Name,
				PublicKey:   req.PublicKey,
				Fingerprint: "SHA256:xyz",
				CreatedAt:   "2025-06-01T12:00:00Z",
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

	// Simulate the create flow
	apiReq := apiCreateSSHKeyRequest{Name: "test-key", PublicKey: "ssh-ed25519 AAAA..."}
	resp, err := c.Post(context.Background(), c.TenantPath("/sshkeys"), apiReq)
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	key, err := client.ParseResponse[apiSSHKey](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if key.ID != "key-abc" {
		t.Errorf("expected ID key-abc, got %s", key.ID)
	}
	if key.Fingerprint != "SHA256:xyz" {
		t.Errorf("expected fingerprint SHA256:xyz, got %s", key.Fingerprint)
	}
}

func TestSSHKeyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/key-abc":
			json.NewEncoder(w).Encode(apiSSHKey{
				ID:          "key-abc",
				Name:        "test-key",
				PublicKey:   "ssh-ed25519 AAAA...",
				Fingerprint: "SHA256:xyz",
				CreatedAt:   "2025-06-01T12:00:00Z",
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

	resp, err := c.Get(context.Background(), c.TenantPath("/sshkeys/key-abc"), nil)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	key, err := client.ParseResponse[apiSSHKey](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if key.ID != "key-abc" {
		t.Errorf("expected ID key-abc, got %s", key.ID)
	}
	if key.Name != "test-key" {
		t.Errorf("expected name test-key, got %s", key.Name)
	}
}

func TestSSHKeyReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "NOT_FOUND",
					"message": "SSH key not found",
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Get(context.Background(), c.TenantPath("/sshkeys/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

func TestSSHKeyDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/key-abc":
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

	_, err := c.Delete(context.Background(), c.TenantPath("/sshkeys/key-abc"))
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if !deleted {
		t.Error("expected delete to be called")
	}
}

// typesStringValue is a helper to create types.String values in tests.
func typesStringValue(s string) types.String {
	return types.StringValue(s)
}
