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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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
	for _, attr := range []string{
		"id", "name", "flavor_id", "image_id", "vpc_id", "ssh_key_ids",
		"security_group_ids", "user_data", "user_data_wo", "user_data_wo_version",
		"metadata", "tags", "created_at", "updated_at",
	} {
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

// configFromPlan mirrors a plan into a Config. Terraform sends both, and the
// two agree on every attribute except the write-only ones, which exist only in
// the config; ltConfig is the helper for a config that carries one.
func configFromPlan(t *testing.T, plan tfsdk.Plan) tfsdk.Config {
	t.Helper()
	return tfsdk.Config(plan)
}

// ltConfig builds the config Terraform sends on the write-only path: the
// document lives in user_data_wo and nowhere else.
func ltConfig(t *testing.T, model LaunchTemplateModel, document string) tfsdk.Config {
	t.Helper()
	model.UserDataWO = types.StringValue(document)
	// tfsdk.Config has no Set; a Plan built from the same model carries an
	// identical raw value, so borrow it.
	return configFromPlan(t, buildLTPlan(t, model))
}

// writeOnlyLTModel is the plan/state shape on the write-only path: user_data and
// user_data_wo both null, only the version companion stored.
func writeOnlyLTModel(version string) LaunchTemplateModel {
	m := fullLTModel()
	m.UserData = types.StringNull()
	m.UserDataWO = types.StringNull()
	m.UserDataWOVer = types.StringValue(version)
	return m
}

// ltJSONWithUserData is what the API actually returns. compute's launch-template
// GET serialises its domain type, which carries userData, so a fixture that
// omits it — as ltJSON does — cannot see a read-path leak at all.
func ltJSONWithUserData(document string) map[string]any {
	return map[string]any{
		"id": "lt-1", "name": "web-lt", "flavorId": "flv-1", "imageId": "img-1",
		"vpcId": "vpc-1", "createdAt": "2025-01-01T00:00:00Z",
		"userData": document,
	}
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
		UserDataWO:       types.StringNull(),
		UserDataWOVer:    types.StringNull(),
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
			// Decoded as a raw map, not back into the struct that encoded it: a
			// renamed json tag round-trips through that struct undetected, and
			// getting the document TO the platform is the half that matters.
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			if raw["name"] != "web-lt" {
				t.Errorf("expected name web-lt, got %v", raw["name"])
			}
			// Pins Create's `!userDataWO.IsNull()` guard: without it a null
			// write-only value overwrites the configured document with "",
			// omitempty then drops it, and the template is created unconfigured
			// while state records the document the practitioner asked for.
			if raw["userData"] != "#cloud-config" {
				t.Errorf("expected the configured userData on the wire, got %v", raw["userData"])
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
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: configFromPlan(t, plan)}, &createResp)
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
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: configFromPlan(t, plan)}, &createResp)
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
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: configFromPlan(t, plan)}, &updateResp)
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
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: configFromPlan(t, plan)}, &updateResp)
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

// --- Write-only user_data (stage 3 of TF-PROVIDER-WRITE-ONLY-SECRETS-PLAN) ---

func ltSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp.Schema
}

func TestSchemaUserDataWriteOnlyAttributes(t *testing.T) {
	attrs := ltSchema(t).Attributes

	legacy, ok := attrs["user_data"].(schema.StringAttribute)
	if !ok {
		t.Fatal("user_data is not a StringAttribute")
	}
	if !legacy.Optional || legacy.Required || legacy.Computed {
		t.Error("user_data must stay Optional-only")
	}
	if !legacy.Sensitive {
		t.Error("user_data must stay Sensitive: the guide says people embed tokens in the document")
	}
	if len(legacy.PlanModifiers) != 0 {
		// UseStateForUnknown here was provably inert — it needs an unknown plan
		// value AND a known config value, which an Optional non-Computed
		// attribute cannot produce — and it is the modifier frostmoln_secret
		// names as what flips a read-path guard from closed to open.
		t.Error("user_data must have no plan modifiers")
	}

	wo, ok := attrs["user_data_wo"].(schema.StringAttribute)
	if !ok {
		t.Fatal("user_data_wo missing from schema")
	}
	if !wo.WriteOnly {
		t.Error("user_data_wo must be WriteOnly")
	}
	if wo.Computed {
		t.Error("user_data_wo must not be Computed: the framework rejects WriteOnly+Computed")
	}
	if !wo.Sensitive {
		t.Error("user_data_wo must be Sensitive")
	}
	if len(wo.PlanModifiers) != 0 {
		// Null in prior state, plan and final state, so a modifier here compares
		// null against null and never fires.
		t.Error("user_data_wo must have no plan modifiers: they can never fire on a write-only attribute")
	}

	ver, ok := attrs["user_data_wo_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("user_data_wo_version missing from schema")
	}
	if ver.WriteOnly {
		t.Error("user_data_wo_version must be stored in state: it is the only change signal")
	}
	if !ver.Optional || ver.Computed {
		t.Error("user_data_wo_version must be Optional-only")
	}
	if ver.Sensitive {
		// The whole "do not derive it from the document" argument depends on
		// this attribute being readable in plan output. Mark it Sensitive and
		// the rationale silently inverts.
		t.Error("user_data_wo_version must NOT be Sensitive")
	}
	if len(ver.PlanModifiers) != 0 {
		// Unlike frostmoln_instance: a launch template updates user data in
		// place, so a changed version must reach Update, not force replacement.
		t.Error("user_data_wo_version must not force replacement: this resource updates in place")
	}
}

// TestUserDataConflictsWithWriteOnly runs the validators, both directions. There
// is no ExactlyOneOf here — user_data was already Optional, so a template with
// no user data at all is valid.
func TestUserDataConflictsWithWriteOnly(t *testing.T) {
	ctx := context.Background()
	sch := ltSchema(t)

	runAttr := func(attrName string, config tfsdk.Config) diag.Diagnostics {
		attr := sch.Attributes[attrName].(schema.StringAttribute)
		var cfgValue types.String
		if d := config.GetAttribute(ctx, path.Root(attrName), &cfgValue); d.HasError() {
			t.Fatalf("reading %s from config: %v", attrName, d.Errors())
		}
		req := validator.StringRequest{
			Config:         config,
			ConfigValue:    cfgValue,
			Path:           path.Root(attrName),
			PathExpression: path.MatchRoot(attrName),
		}
		var resp validator.StringResponse
		for _, v := range attr.Validators {
			v.ValidateString(ctx, req, &resp)
		}
		return resp.Diagnostics
	}

	both := writeOnlyLTModel("1")
	both.UserData = types.StringValue("#cloud-config\n")
	if !runAttr("user_data_wo", ltConfig(t, both, "#cloud-config\n")).HasError() {
		t.Error("expected an error when both user_data and user_data_wo are set")
	}

	noVersion := writeOnlyLTModel("1")
	noVersion.UserDataWOVer = types.StringNull()
	if !runAttr("user_data_wo", ltConfig(t, noVersion, "#cloud-config\n")).HasError() {
		t.Error("expected an error when user_data_wo is set without a version companion")
	}

	if d := runAttr("user_data_wo", ltConfig(t, writeOnlyLTModel("1"), "#cloud-config\n")); d.HasError() {
		t.Errorf("write-only-only config should validate, got %v", d.Errors())
	}

	// The reverse direction. A version companion with no document validates
	// clean without this guard, and the apply is then a silent no-op: nothing is
	// sent, the plan is green, and the practitioner believes the template was
	// updated. Stages 1 and 2 both shipped this half unexecuted.
	noDocument := buildLTPlan(t, writeOnlyLTModel("1")) // user_data_wo null, version set
	if !runAttr("user_data_wo_version", configFromPlan(t, noDocument)).HasError() {
		t.Error("expected an error when user_data_wo_version is set without user_data_wo")
	}
	if d := runAttr("user_data_wo_version", ltConfig(t, writeOnlyLTModel("1"), "#cloud-config\n")); d.HasError() {
		t.Errorf("a version alongside a document should validate, got %v", d.Errors())
	}
}

func TestPreferWriteOnlyAttributeWarnsOnUserData(t *testing.T) {
	ctx := context.Background()
	sch := ltSchema(t)
	legacy := sch.Attributes["user_data"].(schema.StringAttribute)

	req := validator.StringRequest{
		ClientCapabilities: validator.ValidateSchemaClientCapabilities{WriteOnlyAttributesAllowed: true},
		Config:             configFromPlan(t, buildLTPlan(t, fullLTModel())),
		ConfigValue:        types.StringValue("#cloud-config"),
		Path:               path.Root("user_data"),
		PathExpression:     path.MatchRoot("user_data"),
	}
	var resp validator.StringResponse
	for _, v := range legacy.Validators {
		v.ValidateString(ctx, req, &resp)
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("expected a warning nudging a write-only-capable client towards user_data_wo")
	}
	if resp.Diagnostics.HasError() {
		t.Errorf("expected only a warning, got errors: %v", resp.Diagnostics.Errors())
	}
}

// createWriteOnly runs Create on the write-only path against an API that hands
// the document back in its response, and returns the resulting state plus the
// create body that reached the wire.
func createWriteOnly(t *testing.T, document string) (LaunchTemplateModel, apiCreateLaunchTemplateRequest) {
	t.Helper()

	var sent apiCreateLaunchTemplateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/launch-templates" {
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("failed to decode create request: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ltJSONWithUserData(document))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &launchTemplateResource{client: c}
	planModel := writeOnlyLTModel("1")
	planModel.ID = types.StringUnknown()

	createResp := resource.CreateResponse{State: emptyLTState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan:   buildLTPlan(t, planModel),
		Config: ltConfig(t, planModel, document),
	}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result LaunchTemplateModel
	createResp.State.Get(context.Background(), &result)
	return result, sent
}

// TestCreateWriteOnlyKeepsUserDataOutOfState is the point of the whole feature:
// the document reaches the API and nothing derived from it lands in state — even
// though this API returns it in the create response.
func TestCreateWriteOnlyKeepsUserDataOutOfState(t *testing.T) {
	const document = "#cloud-config\nwrite_files:\n  - path: /etc/token\n    content: s3cret\n" // pragma: allowlist secret

	model, sent := createWriteOnly(t, document)

	if sent.UserData != document {
		t.Errorf("expected the write-only document to reach the API, got %q", sent.UserData)
	}
	// This proves the PROVIDER never assigns it. The framework's own
	// NullifyWriteOnlyAttributes is bypassed at this level (the state is built
	// directly), so this is the provider-side half of the guarantee — which is
	// the half a refactor here could break.
	if !model.UserDataWO.IsNull() {
		t.Errorf("user_data_wo must be null in state, got %q", model.UserDataWO.ValueString())
	}
	if !model.UserData.IsNull() {
		t.Errorf("the API's userData was adopted into state: %q", model.UserData.ValueString())
	}
	if model.UserDataWOVer.ValueString() != "1" {
		t.Errorf("expected user_data_wo_version 1 in state, got %q", model.UserDataWOVer)
	}
}

// TestReadIgnoresUserDataFromTheAPI is the stage-1 lesson applied here, and the
// reason apiLaunchTemplate has no userData field: compute's launch-template GET
// serialises a domain type that carries the document, so a refresh that decoded
// and adopted it would defeat the feature on the first plan. The fixture the
// other tests use omits userData entirely and is structurally blind to this.
func TestReadIgnoresUserDataFromTheAPI(t *testing.T) {
	const document = "#cloud-config\nruncmd: [echo leaked]\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1" {
			_ = json.NewEncoder(w).Encode(ltJSONWithUserData(document))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, writeOnlyLTModel("7"))

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result LaunchTemplateModel
	readResp.State.Get(context.Background(), &result)
	if !result.UserData.IsNull() {
		t.Errorf("refresh put the document into state: %q", result.UserData.ValueString())
	}
	if !result.UserDataWO.IsNull() {
		t.Errorf("user_data_wo must stay null across a refresh, got %q", result.UserDataWO.ValueString())
	}
	if result.UserDataWOVer.ValueString() != "7" {
		t.Errorf("user_data_wo_version lost on refresh, got %q", result.UserDataWOVer)
	}
}

// updateWriteOnly runs Update with the given prior and planned version, and
// returns the PATCH body plus the resulting state.
func updateWriteOnly(t *testing.T, stateVersion, planVersion, document string) (apiUpdateLaunchTemplateRequest, LaunchTemplateModel) {
	t.Helper()
	planModel := writeOnlyLTModel(planVersion)
	return runLTUpdate(t, writeOnlyLTModel(stateVersion), planModel, ltConfig(t, planModel, document), document)
}

// runLTUpdate drives Update against an API that hands the document back, with
// the state, plan and config given explicitly — the write-only path's behaviour
// depends on all three disagreeing in specific ways.
func runLTUpdate(t *testing.T, stateModel, planModel LaunchTemplateModel, config tfsdk.Config, document string) (apiUpdateLaunchTemplateRequest, LaunchTemplateModel) {
	t.Helper()

	var patchBody apiUpdateLaunchTemplateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1":
			_ = json.NewDecoder(r.Body).Decode(&patchBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/launch-templates/lt-1":
			_ = json.NewEncoder(w).Encode(ltJSONWithUserData(document))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &launchTemplateResource{client: c}
	state := buildLTState(t, stateModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:   buildLTPlan(t, planModel),
		State:  state,
		Config: config,
	}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	var result LaunchTemplateModel
	updateResp.State.Get(context.Background(), &result)
	return patchBody, result
}

// TestUpdateSendsTheDocumentOnlyWhenTheVersionChanges is the in-place half of
// the shape — the dimension where this resource follows frostmoln_secret rather
// than frostmoln_instance, which replaces instead of updating.
func TestUpdateSendsTheDocumentOnlyWhenTheVersionChanges(t *testing.T) {
	const document = "#cloud-config\npackages: [nginx]\n"

	patchBody, result := updateWriteOnly(t, "1", "2", document)
	if patchBody.UserData == nil {
		t.Fatal("a changed user_data_wo_version must send the current document")
	}
	if *patchBody.UserData != document {
		t.Errorf("expected the write-only document in the patch body, got %q", *patchBody.UserData)
	}
	if !result.UserData.IsNull() || !result.UserDataWO.IsNull() {
		t.Error("neither user_data nor user_data_wo may land in state after an update")
	}
	if result.UserDataWOVer.ValueString() != "2" {
		// A mutant nulling this in Update survives every other test here, and the
		// next plan then reads null -> "2" and updates again on every apply.
		t.Errorf("expected user_data_wo_version 2 in state, got %q", result.UserDataWOVer)
	}

	unchanged, _ := updateWriteOnly(t, "1", "1", document)
	if unchanged.UserData != nil {
		t.Errorf("an unchanged version must leave the stored document alone, sent %q", *unchanged.UserData)
	}
}

// TestPriorLaunchTemplateStateWithoutTheNewAttributesDecodes pins the
// non-breaking claim: a state file written before this change has neither new
// key, and both decode as null against the current schema — so no schema Version
// bump and no state upgrader are needed.
func TestPriorLaunchTemplateStateWithoutTheNewAttributesDecodes(t *testing.T) {
	ctx := context.Background()
	sch := ltSchema(t)

	priorJSON := []byte(`{
		"id": "lt-1", "name": "web-lt", "flavor_id": "flv-1", "image_id": "img-1",
		"vpc_id": "vpc-1", "ssh_key_ids": null, "security_group_ids": null,
		"user_data": "#cloud-config\n", "metadata": null, "tags": null,
		"created_at": "2025-01-01T00:00:00Z", "updated_at": null
	}`)

	typ := sch.Type().TerraformType(ctx)
	raw, err := tftypes.ValueFromJSONWithOpts(priorJSON, typ, tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true})
	if err != nil {
		t.Fatalf("pre-change state does not decode against the new schema: %v", err)
	}

	var model LaunchTemplateModel
	state := tfsdk.State{Schema: sch, Raw: raw}
	if diags := state.Get(ctx, &model); diags.HasError() {
		t.Fatalf("failed to read pre-change state: %v", diags.Errors())
	}
	if !model.UserDataWO.IsNull() || !model.UserDataWOVer.IsNull() {
		t.Error("the new attributes must decode as null from a pre-change state file")
	}
	if model.UserData.ValueString() != "#cloud-config\n" {
		t.Errorf("existing user_data lost in decode, got %q", model.UserData.ValueString())
	}
}

// TestUpdateWithoutTheWriteOnlyPairLeavesTheDocumentAlone pins Update's
// `!userDataWO.IsNull()` guard, which no other test kills. Two reachable
// configs have a null write-only value while the versions differ: removing the
// pair from the configuration, and migrating back to the legacy attribute.
// Without the guard both send `{"userData":""}` — compute assigns it
// unconditionally on a non-nil pointer, so the stored document is wiped and
// every instance launched from the template afterwards boots unconfigured, on a
// green apply.
func TestUpdateWithoutTheWriteOnlyPairLeavesTheDocumentAlone(t *testing.T) {
	planModel := writeOnlyLTModel("1")
	planModel.UserDataWOVer = types.StringNull() // the pair removed from config

	patchBody, _ := runLTUpdate(t, writeOnlyLTModel("1"), planModel,
		configFromPlan(t, buildLTPlan(t, planModel)), "#cloud-config\n")

	if patchBody.UserData != nil {
		t.Errorf("removing the write-only pair must not touch the stored document, sent %q", *patchBody.UserData)
	}
}

// TestUpdateAfterImportSendsTheDocument pins the behaviour the docs promise for
// an imported template: both attributes are null after import, so the first
// apply takes the null -> "1" branch and sends the configured document as an
// ordinary in-place update.
func TestUpdateAfterImportSendsTheDocument(t *testing.T) {
	const document = "#cloud-config\npackages: [nginx]\n"

	stateModel := writeOnlyLTModel("1")
	stateModel.UserDataWOVer = types.StringNull() // as left by terraform import
	planModel := writeOnlyLTModel("1")

	patchBody, result := runLTUpdate(t, stateModel, planModel, ltConfig(t, planModel, document), document)

	if patchBody.UserData == nil || *patchBody.UserData != document {
		t.Errorf("the first apply after import must send the configured document, got %v", patchBody.UserData)
	}
	if result.UserDataWOVer.ValueString() != "1" {
		t.Errorf("expected user_data_wo_version 1 in state, got %q", result.UserDataWOVer)
	}
}

// TestWriteOnlyUserDataRefusesAnEmptyDocument pins LengthAtLeast(1). An
// explicitly empty write-only document is always a mistake — "no user data" is
// expressed by omitting the attribute — and it is the one mistake the write-only
// path hides completely: the plan shows only the version bump, so the apply
// silently clears the stored document.
func TestWriteOnlyUserDataRefusesAnEmptyDocument(t *testing.T) {
	ctx := context.Background()
	sch := ltSchema(t)
	wo := sch.Attributes["user_data_wo"].(schema.StringAttribute)

	model := writeOnlyLTModel("1")
	req := validator.StringRequest{
		Config:         ltConfig(t, model, ""),
		ConfigValue:    types.StringValue(""),
		Path:           path.Root("user_data_wo"),
		PathExpression: path.MatchRoot("user_data_wo"),
	}
	var resp validator.StringResponse
	for _, v := range wo.Validators {
		v.ValidateString(ctx, req, &resp)
	}
	if !resp.Diagnostics.HasError() {
		t.Error("an empty user_data_wo must be refused: it clears the stored document with no plan line to show it")
	}
}

// TestCreateRefusesAnUnknownWriteOnlyValue pins the fail-closed branch in
// writeOnlyUserData. Treating unknown as "no document" would create the template
// without one and report success. Config is fully resolved by apply so this
// should be unreachable in practice — but the branch is copied onward to stage 4,
// and without a test, deleting it survives the suite.
func TestCreateRefusesAnUnknownWriteOnlyValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may reach the platform: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	planModel := writeOnlyLTModel("1")
	planModel.ID = types.StringUnknown()
	configModel := planModel
	configModel.UserDataWO = types.StringUnknown()

	r := &launchTemplateResource{client: c}
	createResp := resource.CreateResponse{State: emptyLTState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan:   buildLTPlan(t, planModel),
		Config: configFromPlan(t, buildLTPlan(t, configModel)),
	}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("an unknown user_data_wo must fail the apply, not create a template with no document")
	}
}
