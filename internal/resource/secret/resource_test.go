package secret

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestSecretModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	model := SecretModel{
		Name:               types.StringValue("db-password"),
		Description:        types.StringValue("the db password"),
		SecretValue:        types.StringValue("s3cr3t"), // pragma: allowlist secret
		ContentType:        types.StringValue("text/plain"),
		Tags:               tags,
		MaxVersions:        types.Int64Value(5),
		RecoveryWindowDays: types.Int64Value(14),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "db-password" {
		t.Errorf("expected name db-password, got %s", req.Name)
	}
	if req.SecretValue != "s3cr3t" { // pragma: allowlist secret
		t.Errorf("expected secretValue s3cr3t, got %s", req.SecretValue)
	}
	if req.Description != "the db password" {
		t.Errorf("expected description, got %s", req.Description)
	}
	if req.ContentType != "text/plain" {
		t.Errorf("expected contentType text/plain, got %s", req.ContentType)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
	if req.MaxVersions != 5 {
		t.Errorf("expected maxVersions 5, got %d", req.MaxVersions)
	}
	if req.RecoveryWindowDays != 14 {
		t.Errorf("expected recoveryWindowDays 14, got %d", req.RecoveryWindowDays)
	}
}

func TestSecretModelToCreateRequestMinimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := SecretModel{
		Name:               types.StringValue("min"),
		SecretValue:        types.StringValue("v"),
		Description:        types.StringNull(),
		ContentType:        types.StringNull(),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Null(),
		RecoveryWindowDays: types.Int64Null(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Description != "" || req.ContentType != "" {
		t.Error("expected empty optional fields for null values")
	}
	if req.Tags != nil {
		t.Error("expected nil tags for null map")
	}
	if req.MaxVersions != 0 || req.RecoveryWindowDays != 0 {
		t.Error("expected zero int fields for null values")
	}
}

func TestSecretModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	planTags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"k": "v2"})
	stateTags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"k": "v1"})

	plan := SecretModel{
		Description:        types.StringValue("new desc"),
		SecretValue:        types.StringValue("newval"), // pragma: allowlist secret
		ContentType:        types.StringValue("application/json"),
		Tags:               planTags,
		MaxVersions:        types.Int64Value(20),
		RecoveryWindowDays: types.Int64Value(30),
	}
	state := SecretModel{
		Description:        types.StringValue("old desc"),
		SecretValue:        types.StringValue("oldval"), // pragma: allowlist secret
		ContentType:        types.StringValue("text/plain"),
		Tags:               stateTags,
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
	}

	req := plan.toUpdateRequest(ctx, &state, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Description == nil || *req.Description != "new desc" {
		t.Error("expected description update")
	}
	if req.SecretValue == nil || *req.SecretValue != "newval" { // pragma: allowlist secret
		t.Error("expected secretValue update")
	}
	if req.ContentType == nil || *req.ContentType != "application/json" {
		t.Error("expected contentType update")
	}
	if req.Tags["k"] != "v2" {
		t.Errorf("expected tag k=v2, got %v", req.Tags)
	}
	if req.MaxVersions == nil || *req.MaxVersions != 20 {
		t.Error("expected maxVersions update")
	}
	if req.RecoveryWindowDays == nil || *req.RecoveryWindowDays != 30 {
		t.Error("expected recoveryWindowDays update")
	}
}

func TestSecretModelToUpdateRequestDescriptionToNull(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	plan := SecretModel{
		Description:        types.StringNull(),
		SecretValue:        types.StringValue("v"),
		ContentType:        types.StringValue("text/plain"),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
	}
	state := SecretModel{
		Description:        types.StringValue("had desc"),
		SecretValue:        types.StringValue("v"),
		ContentType:        types.StringValue("text/plain"),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
	}

	req := plan.toUpdateRequest(ctx, &state, &diags)
	if req.Description == nil || *req.Description != "" {
		t.Error("expected description cleared to empty string")
	}
}

func TestSecretModelToUpdateRequestNoChanges(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	same := SecretModel{
		Description:        types.StringValue("desc"),
		SecretValue:        types.StringValue("v"),
		ContentType:        types.StringValue("text/plain"),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
	}

	req := same.toUpdateRequest(ctx, &same, &diags)
	if req.Description != nil || req.SecretValue != nil || req.ContentType != nil || // pragma: allowlist secret
		req.MaxVersions != nil || req.RecoveryWindowDays != nil || req.Tags != nil {
		t.Errorf("expected empty update request, got %+v", req)
	}
}

func TestSecretModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiSecret{
		ID:                 "secret-1",
		Name:               "my-secret",
		Description:        "desc",
		SecretValue:        "value", // pragma: allowlist secret
		ContentType:        "text/plain",
		Tags:               map[string]string{"env": "prod"},
		MaxVersions:        10,
		RecoveryWindowDays: 7,
		CurrentVersion:     3,
		Status:             "active",
		CreatedAt:          "2025-01-01T00:00:00Z",
		UpdatedAt:          "2025-01-02T00:00:00Z",
	}

	// A non-null secret_value is the legacy path: the practitioner configured it
	// here, so the API's value is adopted for drift detection.
	model := SecretModel{SecretValue: types.StringValue("stale")} // pragma: allowlist secret
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "secret-1" {
		t.Errorf("expected ID secret-1, got %s", model.ID.ValueString())
	}
	if model.Description.ValueString() != "desc" {
		t.Errorf("expected description desc, got %s", model.Description.ValueString())
	}
	if model.SecretValue.ValueString() != "value" { // pragma: allowlist secret
		t.Errorf("expected secret value, got %s", model.SecretValue.ValueString())
	}
	if model.CurrentVersion.ValueInt64() != 3 {
		t.Errorf("expected currentVersion 3, got %d", model.CurrentVersion.ValueInt64())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected updatedAt set, got %s", model.UpdatedAt.ValueString())
	}
	tags := model.Tags.Elements()
	if len(tags) != 1 {
		t.Errorf("expected 1 tag, got %d", len(tags))
	}
}

func TestSecretModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiSecret{
		ID:             "secret-2",
		Name:           "minimal",
		ContentType:    "text/plain",
		MaxVersions:    10,
		CurrentVersion: 1,
		Status:         "active",
		CreatedAt:      "2025-01-01T00:00:00Z",
	}

	var model SecretModel
	model.Description = types.StringNull()
	model.Tags = types.MapNull(types.StringType)
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.Description.IsNull() {
		t.Error("expected null description")
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updatedAt")
	}
	if !model.Tags.IsNull() {
		t.Error("expected null tags")
	}
}

// --- Resource unit tests ---

func TestNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_secret" {
		t.Errorf("expected frostmoln_secret, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	for _, attr := range []string{
		"id", "name", "secret_value", "content_type", "tags",
		"max_versions", "recovery_window_days", "current_version", "status", "created_at", "updated_at",
	} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &secretResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &secretResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: raw},
	}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "secret-123"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "secret-123" {
		t.Errorf("expected imported id secret-123, got %s", id.ValueString())
	}
}

// --- tfsdk helpers ---

func buildSecretState(t *testing.T, model SecretModel) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set state: %v", diags.Errors())
	}
	return state
}

func buildSecretPlan(t *testing.T, model SecretModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := plan.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

// buildSecretConfig returns the configuration Terraform would send for model.
// tfsdk.Config is read-only, so it borrows a plan's encoding of the same model.
func buildSecretConfig(t *testing.T, model SecretModel) tfsdk.Config {
	t.Helper()
	plan := buildSecretPlan(t, model)
	return tfsdk.Config(plan)
}

func emptySecretState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: raw}
}

func fullSecretModel() SecretModel {
	return SecretModel{
		ID:                 types.StringValue("secret-1"),
		Name:               types.StringValue("my-secret"),
		Description:        types.StringValue("desc"),
		SecretValue:        types.StringValue("value"), // pragma: allowlist secret
		ContentType:        types.StringValue("text/plain"),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
		CurrentVersion:     types.Int64Value(1),
		Status:             types.StringValue("active"),
		CreatedAt:          types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:          types.StringNull(),
	}
}

// secretJSONWithValue mirrors the real service, which returns the decrypted
// value on every GET (secrets/internal/service/impl/secret.go, Get). The
// value-less secretJSON below cannot catch a read-path leak at all.
func secretJSONWithValue(status, value string) apiSecret {
	out := secretJSON(status)
	out.SecretValue = value // pragma: allowlist secret
	return out
}

func secretJSON(status string) apiSecret {
	return apiSecret{
		ID:                 "secret-1",
		Name:               "my-secret",
		Description:        "desc",
		ContentType:        "text/plain",
		MaxVersions:        10,
		RecoveryWindowDays: 7,
		CurrentVersion:     1,
		Status:             status,
		CreatedAt:          "2025-01-01T00:00:00Z",
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/secrets" {
			var body apiCreateSecretRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name != "my-secret" {
				t.Errorf("expected name my-secret, got %s", body.Name)
			}
			w.WriteHeader(http.StatusCreated)
			out := secretJSON("active")
			out.SecretValue = body.SecretValue // pragma: allowlist secret
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	planModel := SecretModel{
		Name:               types.StringValue("my-secret"),
		SecretValue:        types.StringValue("value"), // pragma: allowlist secret
		Description:        types.StringValue("desc"),
		ContentType:        types.StringValue("text/plain"),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
	}
	plan := buildSecretPlan(t, planModel)

	createResp := resource.CreateResponse{State: emptySecretState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: buildSecretConfig(t, planModel)}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result SecretModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "secret-1" {
		t.Errorf("expected ID secret-1, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", result.Status.ValueString())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	planModel := SecretModel{
		Name:               types.StringValue("my-secret"),
		SecretValue:        types.StringValue("value"), // pragma: allowlist secret
		Description:        types.StringNull(),
		ContentType:        types.StringValue("text/plain"),
		Tags:               types.MapNull(types.StringType),
		MaxVersions:        types.Int64Value(10),
		RecoveryWindowDays: types.Int64Value(7),
	}
	plan := buildSecretPlan(t, planModel)

	createResp := resource.CreateResponse{State: emptySecretState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: buildSecretConfig(t, planModel)}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/secrets/secret-1" {
			_ = json.NewEncoder(w).Encode(secretJSON("active"))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	state := buildSecretState(t, fullSecretModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result SecretModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", result.Status.ValueString())
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	state := buildSecretState(t, fullSecretModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	var result SecretModel
	if diags := readResp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestUpdate(t *testing.T) {
	var putBody apiUpdateSecretRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/secrets/secret-1":
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/secrets/secret-1":
			out := secretJSON("active")
			out.Description = "new desc"
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}

	state := buildSecretState(t, fullSecretModel())
	planModel := fullSecretModel()
	planModel.Description = types.StringValue("new desc")
	plan := buildSecretPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: buildSecretConfig(t, planModel)}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if putBody.Description == nil || *putBody.Description != "new desc" {
		t.Error("expected description in update body")
	}
}

func TestUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	state := buildSecretState(t, fullSecretModel())
	planModel := fullSecretModel()
	planModel.Description = types.StringValue("new desc")
	plan := buildSecretPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: buildSecretConfig(t, planModel)}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error on update API failure")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/secrets/secret-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	state := buildSecretState(t, fullSecretModel())

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	state := buildSecretState(t, fullSecretModel())

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of gone resource should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}

// --- write-only secret_value_wo ---

func writeOnlyModel(version string) SecretModel {
	m := fullSecretModel()
	m.SecretValue = types.StringNull()
	m.SecretValueWO = types.StringValue("wo-value") // pragma: allowlist secret
	m.SecretValueWOVer = types.StringValue(version)
	return m
}

func TestSchemaWriteOnlyAttributes(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	legacy, ok := resp.Schema.Attributes["secret_value"].(schema.StringAttribute)
	if !ok {
		t.Fatal("secret_value is not a StringAttribute")
	}
	if legacy.Required {
		t.Error("secret_value must be Optional now that secret_value_wo exists")
	}
	if !legacy.Optional {
		t.Error("secret_value must be Optional")
	}
	if legacy.Computed {
		// The write-only guard in fromAPI keys on secret_value being null. A
		// Computed secret_value would be marked unknown instead of null on the
		// write-only path, so the guard would pass and adopt the plaintext.
		t.Error("secret_value must not be Computed: the write-only guard keys on a null plan value")
	}
	if len(legacy.PlanModifiers) != 0 {
		t.Error("secret_value must have no plan modifiers: UseStateForUnknown would defeat the write-only guard")
	}

	wo, ok := resp.Schema.Attributes["secret_value_wo"].(schema.StringAttribute)
	if !ok {
		t.Fatal("secret_value_wo missing from schema")
	}
	if !wo.WriteOnly {
		t.Error("secret_value_wo must be WriteOnly")
	}
	if wo.Computed {
		t.Error("secret_value_wo must not be Computed: the framework rejects WriteOnly+Computed")
	}
	if !wo.Sensitive {
		t.Error("secret_value_wo must be Sensitive")
	}

	ver, ok := resp.Schema.Attributes["secret_value_wo_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("secret_value_wo_version missing from schema")
	}
	if ver.WriteOnly {
		t.Error("secret_value_wo_version must be stored in state: it is the only change signal")
	}
	if !ver.Optional || ver.Computed {
		t.Error("secret_value_wo_version must be Optional-only")
	}
}

func TestConfigValidatorsExactlyOneSecretValue(t *testing.T) {
	validators := NewResource().(resource.ResourceWithConfigValidators).ConfigValidators(context.Background())
	if len(validators) == 0 {
		t.Fatal("expected at least one config validator")
	}

	run := func(model SecretModel) diag.Diagnostics {
		req := resource.ValidateConfigRequest{Config: buildSecretConfig(t, model)}
		var resp resource.ValidateConfigResponse
		for _, v := range validators {
			v.ValidateResource(context.Background(), req, &resp)
		}
		return resp.Diagnostics
	}

	both := writeOnlyModel("1")
	both.SecretValue = types.StringValue("legacy") // pragma: allowlist secret
	if !run(both).HasError() {
		t.Error("expected an error when both secret_value and secret_value_wo are set")
	}

	neither := fullSecretModel()
	neither.SecretValue = types.StringNull()
	if !run(neither).HasError() {
		t.Error("expected an error when neither secret_value nor secret_value_wo is set")
	}

	if d := run(writeOnlyModel("1")); d.HasError() {
		t.Errorf("write-only-only config should validate, got %v", d.Errors())
	}
	if d := run(fullSecretModel()); d.HasError() {
		t.Errorf("legacy-only config should validate, got %v", d.Errors())
	}
}

func TestPreferWriteOnlyAttributeWarnsOnLegacyValue(t *testing.T) {
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	attr := schemaResp.Schema.Attributes["secret_value"].(schema.StringAttribute)

	req := validator.StringRequest{
		ClientCapabilities: validator.ValidateSchemaClientCapabilities{WriteOnlyAttributesAllowed: true},
		Config:             buildSecretConfig(t, fullSecretModel()),
		ConfigValue:        types.StringValue("value"), // pragma: allowlist secret
		Path:               path.Root("secret_value"),
		PathExpression:     path.MatchRoot("secret_value"),
	}
	var resp validator.StringResponse
	for _, v := range attr.Validators {
		v.ValidateString(context.Background(), req, &resp)
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("expected a warning nudging a write-only-capable client towards secret_value_wo")
	}
	if resp.Diagnostics.HasError() {
		t.Errorf("expected only a warning, got errors: %v", resp.Diagnostics.Errors())
	}
}

// TestCreateWriteOnlyKeepsValueOutOfState is the point of the whole feature: the
// value reaches the API, and neither it nor secret_value survives into state.
func TestCreateWriteOnlyKeepsValueOutOfState(t *testing.T) {
	var sent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/secrets" {
			var body apiCreateSecretRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = body.SecretValue
			w.WriteHeader(http.StatusCreated)
			out := secretJSON("active")
			// The API echoes the value back on create; state must not keep it.
			out.SecretValue = body.SecretValue // pragma: allowlist secret
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &secretResource{client: c}
	model := writeOnlyModel("1")

	// Terraform nulls a write-only attribute in the plan; only the config has it.
	planModel := model
	planModel.SecretValueWO = types.StringNull()

	createResp := resource.CreateResponse{State: emptySecretState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan:   buildSecretPlan(t, planModel),
		Config: buildSecretConfig(t, model),
	}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	if sent != "wo-value" { // pragma: allowlist secret
		t.Errorf("expected the write-only value to reach the API, got %q", sent)
	}

	var result SecretModel
	createResp.State.Get(context.Background(), &result)
	if !result.SecretValueWO.IsNull() {
		t.Errorf("secret_value_wo must be null in state, got %q", result.SecretValueWO.ValueString())
	}
	if !result.SecretValue.IsNull() {
		t.Errorf("secret_value must stay null on the write-only path, got %q", result.SecretValue.ValueString())
	}
	if result.SecretValueWOVer.ValueString() != "1" {
		t.Errorf("expected secret_value_wo_version 1 in state, got %q", result.SecretValueWOVer)
	}
}

func TestUpdateWriteOnlySendsValueOnlyWhenVersionChanges(t *testing.T) {
	var putBody apiUpdateSecretRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			putBody = apiUpdateSecretRequest{}
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(secretJSON("active"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &secretResource{client: c}

	apply := func(stateVersion, planVersion string) SecretModel {
		t.Helper()
		stateModel := writeOnlyModel(stateVersion)
		stateModel.SecretValueWO = types.StringNull() // never persisted
		state := buildSecretState(t, stateModel)

		config := writeOnlyModel(planVersion)
		planModel := config
		planModel.SecretValueWO = types.StringNull()

		updateResp := resource.UpdateResponse{State: state}
		r.Update(context.Background(), resource.UpdateRequest{
			Plan:   buildSecretPlan(t, planModel),
			State:  state,
			Config: buildSecretConfig(t, config),
		}, &updateResp)
		if updateResp.Diagnostics.HasError() {
			t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
		}
		var result SecretModel
		updateResp.State.Get(context.Background(), &result)
		return result
	}

	result := apply("1", "2")
	if putBody.SecretValue == nil || *putBody.SecretValue != "wo-value" { // pragma: allowlist secret
		t.Errorf("a changed version must push the write-only value, got %v", putBody.SecretValue)
	}
	if !result.SecretValue.IsNull() || !result.SecretValueWO.IsNull() {
		t.Error("neither secret value may land in state after a write-only update")
	}
	if result.SecretValueWOVer.ValueString() != "2" {
		t.Errorf("expected the new version in state, got %q", result.SecretValueWOVer)
	}

	apply("1", "1")
	if putBody.SecretValue != nil { // pragma: allowlist secret
		t.Errorf("an unchanged version must not push a value, got %q", *putBody.SecretValue) // pragma: allowlist secret
	}
}

// TestReadWriteOnlyKeepsValueOutOfState covers the path Create cannot: Read has
// no config, so the only discriminator is that prior state left secret_value
// null. The API hands back the plaintext on every refresh.
func TestReadWriteOnlyKeepsValueOutOfState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(secretJSONWithValue("active", "wo-value"))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &secretResource{client: c}

	stateModel := writeOnlyModel("1")
	stateModel.SecretValueWO = types.StringNull() // never persisted
	state := buildSecretState(t, stateModel)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result SecretModel
	readResp.State.Get(context.Background(), &result)
	if !result.SecretValue.IsNull() {
		t.Errorf("refresh put the secret back into state: %q", result.SecretValue.ValueString())
	}
	if !result.SecretValueWO.IsNull() {
		t.Errorf("secret_value_wo must stay null, got %q", result.SecretValueWO.ValueString())
	}
}

// TestReadLegacyPathKeepsValue pins the other direction: a practitioner who
// configured secret_value still gets drift detection from the API's value.
func TestReadLegacyPathKeepsValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(secretJSONWithValue("active", "rotated-elsewhere"))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &secretResource{client: c}

	state := buildSecretState(t, fullSecretModel())
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result SecretModel
	readResp.State.Get(context.Background(), &result)
	if result.SecretValue.ValueString() != "rotated-elsewhere" {
		t.Errorf("legacy path lost drift detection, got %q", result.SecretValue.ValueString())
	}
}

// TestUpdateNullSecretValueIsNeverSentAsEmpty is the data-loss guard: a null
// secret_value means "not the source", and the API writes "" as a new version
// with no way back.
func TestUpdateNullSecretValueIsNeverSentAsEmpty(t *testing.T) {
	var putBody apiUpdateSecretRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		default:
			_ = json.NewEncoder(w).Encode(secretJSONWithValue("active", "wo-value"))
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &secretResource{client: c}

	// Worst case: state still holds a value (a pre-fix state file, or a legacy
	// resource being migrated), config has it null, version unchanged.
	stateModel := writeOnlyModel("1")
	stateModel.SecretValueWO = types.StringNull()
	stateModel.SecretValue = types.StringValue("live-secret") // pragma: allowlist secret
	state := buildSecretState(t, stateModel)

	config := writeOnlyModel("1")
	planModel := config
	planModel.SecretValueWO = types.StringNull()

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:   buildSecretPlan(t, planModel),
		State:  state,
		Config: buildSecretConfig(t, config),
	}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if putBody.SecretValue != nil { // pragma: allowlist secret
		t.Errorf("a null secret_value must never be sent, got %q", *putBody.SecretValue)
	}
}

// TestUpdateMigrationFromLegacySendsTheWriteOnlyValue covers the switch an
// existing practitioner makes: secret_value removed, secret_value_wo added. The
// state version is null, so the version always differs and the value is pushed.
func TestUpdateMigrationFromLegacySendsTheWriteOnlyValue(t *testing.T) {
	var putBody apiUpdateSecretRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		default:
			_ = json.NewEncoder(w).Encode(secretJSONWithValue("active", "wo-value"))
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &secretResource{client: c}

	state := buildSecretState(t, fullSecretModel()) // legacy: secret_value set, no version

	config := writeOnlyModel("1")
	planModel := config
	planModel.SecretValueWO = types.StringNull()

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:   buildSecretPlan(t, planModel),
		State:  state,
		Config: buildSecretConfig(t, config),
	}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if putBody.SecretValue == nil || *putBody.SecretValue != "wo-value" { // pragma: allowlist secret
		t.Errorf("migration must push the write-only value, got %v", putBody.SecretValue)
	}

	var result SecretModel
	updateResp.State.Get(context.Background(), &result)
	if !result.SecretValue.IsNull() {
		t.Errorf("secret_value must be null after migrating, got %q", result.SecretValue.ValueString())
	}
}

// TestPriorStateWithoutTheNewAttributesDecodes pins the non-breaking claim: a
// state file written before this change has neither new key, and both decode as
// null against the current schema — so no schema Version bump is needed.
func TestPriorStateWithoutTheNewAttributesDecodes(t *testing.T) {
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	priorJSON := []byte(`{
		"id": "secret-1", "name": "my-secret", "description": "desc",
		"secret_value": "value", "content_type": "text/plain", "tags": null,
		"max_versions": 10, "recovery_window_days": 7, "current_version": 1,
		"status": "active", "created_at": "2025-01-01T00:00:00Z", "updated_at": null
	}`) // pragma: allowlist secret

	typ := schemaResp.Schema.Type().TerraformType(context.Background())
	raw, err := tftypes.ValueFromJSONWithOpts(priorJSON, typ, tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true})
	if err != nil {
		t.Fatalf("pre-change state does not decode against the new schema: %v", err)
	}

	var model SecretModel
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: raw}
	if diags := state.Get(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to read pre-change state: %v", diags.Errors())
	}
	if !model.SecretValueWO.IsNull() || !model.SecretValueWOVer.IsNull() {
		t.Error("the new attributes must decode as null from a pre-change state file")
	}
	if model.SecretValue.ValueString() != "value" { // pragma: allowlist secret
		t.Errorf("existing secret_value lost in decode, got %q", model.SecretValue.ValueString())
	}
}

// TestSecretModelFromAPIKeepsNullSecretValueNull is the unit-level form of the
// write-only guard: the API hands back the plaintext and a null secret_value
// must survive it. Covered end to end by the Read/Create/Update tests too, but
// this is the one that names the rule.
func TestSecretModelFromAPIKeepsNullSecretValueNull(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := secretJSONWithValue("active", "wo-value")
	model := SecretModel{SecretValue: types.StringNull()}
	model.fromAPI(ctx, &api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if !model.SecretValue.IsNull() {
		t.Errorf("a null secret_value must not adopt the API value, got %q", model.SecretValue.ValueString())
	}
}
