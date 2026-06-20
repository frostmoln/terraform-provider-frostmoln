package ssh_key

import (
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
			_ = json.NewEncoder(w).Encode(map[string]string{
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
			_ = json.NewEncoder(w).Encode(apiSSHKey{
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
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/key-abc":
			_ = json.NewEncoder(w).Encode(apiSSHKey{
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
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
			_ = json.NewEncoder(w).Encode(map[string]string{
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

// --- Resource method tests (tfsdk-level) ---

func newMeAndSSHKeyServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
			return
		}
		handler(w, r)
	}))
}

func configuredSSHKeyResource(t *testing.T, serverURL string) *sshKeyResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := &sshKeyResource{client: c}
	return r
}

func sshKeySchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &sshKeyResource{}
	schemaResp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp.Schema
}

func sshKeyTFType(t *testing.T) tftypes.Type {
	t.Helper()
	s := sshKeySchema(t)
	return s.Type().TerraformType(context.Background())
}

func TestSSHKeyResource_NewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
	if _, ok := r.(*sshKeyResource); !ok {
		t.Fatalf("expected *sshKeyResource, got %T", r)
	}
}

func TestSSHKeyResource_Metadata(t *testing.T) {
	r := &sshKeyResource{}
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := resource.MetadataResponse{}
	r.Metadata(context.Background(), req, &resp)

	if resp.TypeName != "frostmoln_ssh_key" {
		t.Errorf("expected type name frostmoln_ssh_key, got %s", resp.TypeName)
	}
}

func TestSSHKeyResource_Schema(t *testing.T) {
	r := &sshKeyResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	if resp.Schema.Description == "" {
		t.Error("expected non-empty schema description")
	}
	attrs := resp.Schema.Attributes
	for _, name := range []string{"id", "name", "public_key", "fingerprint", "created_at"} {
		if _, ok := attrs[name]; !ok {
			t.Errorf("expected attribute %s in schema", name)
		}
	}
}

func TestSSHKeyResource_Configure_NilProviderData(t *testing.T) {
	r := &sshKeyResource{}
	req := resource.ConfigureRequest{ProviderData: nil}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no errors with nil provider data, got %v", resp.Diagnostics.Errors())
	}
	if r.client != nil {
		t.Error("expected nil client after nil provider data")
	}
}

func TestSSHKeyResource_Configure_ValidClient(t *testing.T) {
	r := &sshKeyResource{}
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

func TestSSHKeyResource_Configure_WrongType(t *testing.T) {
	r := &sshKeyResource{}
	req := resource.ConfigureRequest{ProviderData: "not-a-client"}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for wrong provider data type")
	}
}

func TestSSHKeyResource_Create_TFSDK(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/sshkeys":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiSSHKey{
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
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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

	var model SSHKeyModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "key-abc" {
		t.Errorf("expected ID key-abc, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-key" {
		t.Errorf("expected name test-key, got %s", model.Name.ValueString())
	}
	if model.Fingerprint.ValueString() != "SHA256:xyz" {
		t.Errorf("expected fingerprint SHA256:xyz, got %s", model.Fingerprint.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-06-01T12:00:00Z" {
		t.Errorf("expected created_at, got %s", model.CreatedAt.ValueString())
	}
}

func TestSSHKeyResource_Create_APIError(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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

func TestSSHKeyResource_Read_TFSDK(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/key-abc":
			_ = json.NewEncoder(w).Encode(apiSSHKey{
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
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "key-abc"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
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

	var model SSHKeyModel
	readResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "key-abc" {
		t.Errorf("expected ID key-abc, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-key" {
		t.Errorf("expected name test-key, got %s", model.Name.ValueString())
	}
}

func TestSSHKeyResource_Read_NotFound_TFSDK(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "nonexistent"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
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
		t.Fatalf("expected no errors on not-found (resource removed), got %v", readResp.Diagnostics.Errors())
	}

	// State should be empty (resource removed).
	var model SSHKeyModel
	diags := readResp.State.Get(ctx, &model)
	if !diags.HasError() {
		// If the state is empty, Get should return an error.
		// But with the framework, RemoveResource sets the raw to nil.
		// Check that the raw state is null.
		if !readResp.State.Raw.IsNull() {
			t.Error("expected null state after not-found read")
		}
	}
}

func TestSSHKeyResource_Read_APIError(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "key-abc"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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

func TestSSHKeyResource_Update_TFSDK(t *testing.T) {
	r := &sshKeyResource{}
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

func TestSSHKeyResource_Delete_TFSDK(t *testing.T) {
	deleted := false
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/sshkeys/key-abc":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "key-abc"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
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

func TestSSHKeyResource_Delete_NotFound_TFSDK(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "nonexistent"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	// Delete of a not-found resource should succeed silently.
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors on delete of nonexistent resource, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestSSHKeyResource_Delete_APIError(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSSHKeyResource(t, server.URL)
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "key-abc"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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

func TestSSHKeyResource_ImportState_TFSDK(t *testing.T) {
	r := &sshKeyResource{}
	s := sshKeySchema(t)
	tfType := sshKeyTFType(t)

	// Initialize state with null values so the schema type is set.
	initVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"public_key":  tftypes.NewValue(tftypes.String, nil),
		"fingerprint": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	importReq := resource.ImportStateRequest{ID: "key-abc"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: initVal},
	}

	r.ImportState(context.Background(), importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", importResp.Diagnostics.Errors())
	}

	var model SSHKeyModel
	importResp.State.Get(context.Background(), &model)
	if model.ID.ValueString() != "key-abc" {
		t.Errorf("expected imported ID key-abc, got %s", model.ID.ValueString())
	}
}
