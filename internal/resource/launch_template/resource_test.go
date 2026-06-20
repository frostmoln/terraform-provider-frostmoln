package launch_template

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestLaunchTemplateModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	keys, _ := types.SetValueFrom(ctx, types.StringType, []string{"key-1"})
	sgs, _ := types.SetValueFrom(ctx, types.StringType, []string{"sg-1"})
	meta, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"role": "web"})
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := LaunchTemplateModel{
		Name:             types.StringValue("web-lt"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        keys,
		SecurityGroupIDs: sgs,
		UserData:         types.StringValue("#cloud-config"),
		Metadata:         meta,
		Tags:             tags,
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name != "web-lt" || req.FlavorID != "flv-1" || req.ImageID != "img-1" || req.VPCID != "vpc-1" {
		t.Errorf("unexpected required fields: %+v", req)
	}
	if len(req.SSHKeyIDs) != 1 || req.SSHKeyIDs[0] != "key-1" {
		t.Errorf("expected ssh key, got %v", req.SSHKeyIDs)
	}
	if len(req.SecurityGroupIDs) != 1 || req.SecurityGroupIDs[0] != "sg-1" {
		t.Errorf("expected sg, got %v", req.SecurityGroupIDs)
	}
	if req.UserData != "#cloud-config" {
		t.Errorf("expected userData, got %s", req.UserData)
	}
	if req.Metadata["role"] != "web" {
		t.Errorf("expected metadata role=web, got %v", req.Metadata)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestLaunchTemplateModelToCreateRequestMinimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := LaunchTemplateModel{
		Name:             types.StringValue("min"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        types.SetNull(types.StringType),
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringNull(),
		Metadata:         types.MapNull(types.StringType),
		Tags:             types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.SSHKeyIDs != nil || req.SecurityGroupIDs != nil || req.Metadata != nil || req.Tags != nil {
		t.Error("expected nil optional collections")
	}
	if req.UserData != "" {
		t.Error("expected empty userData")
	}
}

func TestLaunchTemplateModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	planKeys, _ := types.SetValueFrom(ctx, types.StringType, []string{"key-2"})
	stateKeys, _ := types.SetValueFrom(ctx, types.StringType, []string{"key-1"})
	planMeta, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"a": "2"})
	stateMeta, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"a": "1"})

	plan := LaunchTemplateModel{
		Name:             types.StringValue("new"),
		FlavorID:         types.StringValue("flv-2"),
		ImageID:          types.StringValue("img-2"),
		VPCID:            types.StringValue("vpc-2"),
		SSHKeyIDs:        planKeys,
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringValue("data"),
		Metadata:         planMeta,
		Tags:             types.MapNull(types.StringType),
	}
	state := LaunchTemplateModel{
		Name:             types.StringValue("old"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        stateKeys,
		SecurityGroupIDs: stateKeys,
		UserData:         types.StringNull(),
		Metadata:         stateMeta,
		Tags:             types.MapNull(types.StringType),
	}

	req := plan.toUpdateRequest(ctx, &state, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name == nil || *req.Name != "new" {
		t.Error("expected name update")
	}
	if req.FlavorID == nil || *req.FlavorID != "flv-2" {
		t.Error("expected flavor update")
	}
	if req.ImageID == nil || *req.ImageID != "img-2" {
		t.Error("expected image update")
	}
	if req.VPCID == nil || *req.VPCID != "vpc-2" {
		t.Error("expected vpc update")
	}
	if len(req.SSHKeyIDs) != 1 || req.SSHKeyIDs[0] != "key-2" {
		t.Errorf("expected ssh key update, got %v", req.SSHKeyIDs)
	}
	// security group changed from set to null -> empty slice
	if req.SecurityGroupIDs == nil || len(req.SecurityGroupIDs) != 0 {
		t.Errorf("expected empty sg slice, got %v", req.SecurityGroupIDs)
	}
	if req.UserData == nil || *req.UserData != "data" {
		t.Error("expected userData update")
	}
	if req.Metadata["a"] != "2" {
		t.Errorf("expected metadata update, got %v", req.Metadata)
	}
}

func TestLaunchTemplateModelToUpdateRequestNoChanges(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	same := LaunchTemplateModel{
		Name:             types.StringValue("same"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        types.SetNull(types.StringType),
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringNull(),
		Metadata:         types.MapNull(types.StringType),
		Tags:             types.MapNull(types.StringType),
	}

	req := same.toUpdateRequest(ctx, &same, &diags)
	if req.Name != nil || req.FlavorID != nil || req.ImageID != nil || req.VPCID != nil ||
		req.SSHKeyIDs != nil || req.SecurityGroupIDs != nil || req.UserData != nil ||
		req.Metadata != nil || req.Tags != nil {
		t.Errorf("expected empty update request, got %+v", req)
	}
}

func TestLaunchTemplateModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiLaunchTemplate{
		ID:               "lt-1",
		Name:             "web-lt",
		FlavorID:         "flv-1",
		ImageID:          "img-1",
		VPCID:            "vpc-1",
		SSHKeyIDs:        []string{"key-1"},
		SecurityGroupIDs: []string{"sg-1"},
		Metadata:         map[string]string{"role": "web"},
		Tags:             map[string]string{"env": "prod"},
		CreatedAt:        "2025-01-01T00:00:00Z",
		UpdatedAt:        "2025-01-02T00:00:00Z",
	}

	var model LaunchTemplateModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if model.ID.ValueString() != "lt-1" {
		t.Errorf("expected ID lt-1, got %s", model.ID.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected updatedAt set, got %s", model.UpdatedAt.ValueString())
	}
	if len(model.SSHKeyIDs.Elements()) != 1 {
		t.Errorf("expected 1 ssh key, got %d", len(model.SSHKeyIDs.Elements()))
	}
	if len(model.Metadata.Elements()) != 1 {
		t.Errorf("expected 1 metadata entry, got %d", len(model.Metadata.Elements()))
	}
}

func TestLaunchTemplateModelFromAPIEmptiesPriorPopulated(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	// API returns empty collections, but state had them populated (non-null) ->
	// model should hold empty (non-null) sets/maps, not null.
	api := &apiLaunchTemplate{
		ID:        "lt-3",
		Name:      "lt",
		FlavorID:  "flv-1",
		ImageID:   "img-1",
		VPCID:     "vpc-1",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	model := LaunchTemplateModel{}
	model.SSHKeyIDs, _ = types.SetValueFrom(ctx, types.StringType, []string{"k"})
	model.SecurityGroupIDs, _ = types.SetValueFrom(ctx, types.StringType, []string{"sg"})
	model.Metadata, _ = types.MapValueFrom(ctx, types.StringType, map[string]string{"a": "1"})
	model.Tags, _ = types.MapValueFrom(ctx, types.StringType, map[string]string{"e": "p"})

	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if model.SSHKeyIDs.IsNull() || len(model.SSHKeyIDs.Elements()) != 0 {
		t.Error("expected empty (non-null) ssh keys")
	}
	if model.SecurityGroupIDs.IsNull() || len(model.SecurityGroupIDs.Elements()) != 0 {
		t.Error("expected empty (non-null) security groups")
	}
	if model.Metadata.IsNull() || len(model.Metadata.Elements()) != 0 {
		t.Error("expected empty (non-null) metadata")
	}
	if model.Tags.IsNull() || len(model.Tags.Elements()) != 0 {
		t.Error("expected empty (non-null) tags")
	}
}

func TestLaunchTemplateModelToUpdateRequestCollectionsToNull(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	stateKeys, _ := types.SetValueFrom(ctx, types.StringType, []string{"k"})
	stateMeta, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"a": "1"})
	stateTags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"e": "p"})

	// plan clears all collections (null) while state had them.
	plan := LaunchTemplateModel{
		Name:             types.StringValue("same"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        types.SetNull(types.StringType),
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringNull(),
		Metadata:         types.MapNull(types.StringType),
		Tags:             types.MapNull(types.StringType),
	}
	state := LaunchTemplateModel{
		Name:             types.StringValue("same"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        stateKeys,
		SecurityGroupIDs: stateKeys,
		UserData:         types.StringNull(),
		Metadata:         stateMeta,
		Tags:             stateTags,
	}

	req := plan.toUpdateRequest(ctx, &state, &diags)
	if req.SSHKeyIDs == nil || len(req.SSHKeyIDs) != 0 {
		t.Errorf("expected empty ssh slice, got %v", req.SSHKeyIDs)
	}
	if req.SecurityGroupIDs == nil || len(req.SecurityGroupIDs) != 0 {
		t.Errorf("expected empty sg slice, got %v", req.SecurityGroupIDs)
	}
	if req.Metadata == nil || len(req.Metadata) != 0 {
		t.Errorf("expected empty metadata map, got %v", req.Metadata)
	}
	if req.Tags == nil || len(req.Tags) != 0 {
		t.Errorf("expected empty tags map, got %v", req.Tags)
	}
}

func TestLaunchTemplateModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiLaunchTemplate{
		ID:        "lt-2",
		Name:      "min",
		FlavorID:  "flv-1",
		ImageID:   "img-1",
		VPCID:     "vpc-1",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model LaunchTemplateModel
	model.SSHKeyIDs = types.SetNull(types.StringType)
	model.SecurityGroupIDs = types.SetNull(types.StringType)
	model.Metadata = types.MapNull(types.StringType)
	model.Tags = types.MapNull(types.StringType)
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updatedAt")
	}
	if !model.SSHKeyIDs.IsNull() {
		t.Error("expected null ssh keys")
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
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_launch_template" {
		t.Errorf("expected frostmoln_launch_template, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "name", "flavor_id", "image_id", "vpc_id", "ssh_key_ids",
		"security_group_ids", "user_data", "metadata", "tags", "created_at", "updated_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &launchTemplateResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &launchTemplateResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: raw}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "lt-123"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "lt-123" {
		t.Errorf("expected imported id lt-123, got %s", id.ValueString())
	}
}

// --- tfsdk helpers ---

func buildLTState(t *testing.T, model LaunchTemplateModel) tfsdk.State {
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

func buildLTPlan(t *testing.T, model LaunchTemplateModel) tfsdk.Plan {
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

func emptyLTState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: raw}
}

func fullLTModel() LaunchTemplateModel {
	return LaunchTemplateModel{
		ID:               types.StringValue("lt-1"),
		Name:             types.StringValue("web-lt"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        types.SetNull(types.StringType),
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringValue("#cloud-config"),
		Metadata:         types.MapNull(types.StringType),
		Tags:             types.MapNull(types.StringType),
		CreatedAt:        types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:        types.StringNull(),
	}
}

func ltJSON() apiLaunchTemplate {
	return apiLaunchTemplate{
		ID:        "lt-1",
		Name:      "web-lt",
		FlavorID:  "flv-1",
		ImageID:   "img-1",
		VPCID:     "vpc-1",
		CreatedAt: "2025-01-01T00:00:00Z",
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/launch-templates" {
			var body apiCreateLaunchTemplateRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name != "web-lt" {
				t.Errorf("expected name web-lt, got %s", body.Name)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ltJSON())
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &launchTemplateResource{client: c}
	plan := buildLTPlan(t, LaunchTemplateModel{
		Name:             types.StringValue("web-lt"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        types.SetNull(types.StringType),
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringValue("#cloud-config"),
		Metadata:         types.MapNull(types.StringType),
		Tags:             types.MapNull(types.StringType),
	})

	createResp := resource.CreateResponse{State: emptyLTState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
	var result LaunchTemplateModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "lt-1" {
		t.Errorf("expected ID lt-1, got %s", result.ID.ValueString())
	}
	// user_data is write-only and preserved from plan.
	if result.UserData.ValueString() != "#cloud-config" {
		t.Errorf("expected userData preserved, got %s", result.UserData.ValueString())
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

	r := &launchTemplateResource{client: c}
	plan := buildLTPlan(t, LaunchTemplateModel{
		Name:             types.StringValue("web-lt"),
		FlavorID:         types.StringValue("flv-1"),
		ImageID:          types.StringValue("img-1"),
		VPCID:            types.StringValue("vpc-1"),
		SSHKeyIDs:        types.SetNull(types.StringType),
		SecurityGroupIDs: types.SetNull(types.StringType),
		UserData:         types.StringNull(),
		Metadata:         types.MapNull(types.StringType),
		Tags:             types.MapNull(types.StringType),
	})

	createResp := resource.CreateResponse{State: emptyLTState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1" {
			_ = json.NewEncoder(w).Encode(ltJSON())
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, fullLTModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result LaunchTemplateModel
	readResp.State.Get(context.Background(), &result)
	if result.Name.ValueString() != "web-lt" {
		t.Errorf("expected name web-lt, got %s", result.Name.ValueString())
	}
	// write-only user_data preserved
	if result.UserData.ValueString() != "#cloud-config" {
		t.Errorf("expected userData preserved, got %s", result.UserData.ValueString())
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

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, fullLTModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	var result LaunchTemplateModel
	if diags := readResp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestUpdate(t *testing.T) {
	var patchBody apiUpdateLaunchTemplateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1":
			_ = json.NewDecoder(r.Body).Decode(&patchBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1":
			out := ltJSON()
			out.Name = "renamed-lt"
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, fullLTModel())
	planModel := fullLTModel()
	planModel.Name = types.StringValue("renamed-lt")
	plan := buildLTPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if patchBody.Name == nil || *patchBody.Name != "renamed-lt" {
		t.Error("expected name in patch body")
	}
	var result LaunchTemplateModel
	updateResp.State.Get(context.Background(), &result)
	if result.Name.ValueString() != "renamed-lt" {
		t.Errorf("expected name renamed-lt, got %s", result.Name.ValueString())
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

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, fullLTModel())
	planModel := fullLTModel()
	planModel.Name = types.StringValue("renamed-lt")
	plan := buildLTPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error on update API failure")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1" {
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

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, fullLTModel())

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

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, fullLTModel())

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of gone resource should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}
