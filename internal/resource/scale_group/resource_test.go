package scale_group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestScaleGroupModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	subnets, _ := types.SetValueFrom(ctx, types.StringType, []string{"sn-1"})
	pools, _ := types.SetValueFrom(ctx, types.StringType, []string{"pool-1"})
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := ScaleGroupModel{
		Name:                   types.StringValue("asg"),
		LaunchTemplateID:       types.StringValue("lt-1"),
		MinSize:                types.Int64Value(1),
		MaxSize:                types.Int64Value(5),
		DesiredCapacity:        types.Int64Value(2),
		SubnetIDs:              subnets,
		LoadBalancerPoolIDs:    pools,
		HealthCheckType:        types.StringValue("lb"),
		HealthCheckGracePeriod: types.Int64Value(120),
		WarmupSeconds:          types.Int64Value(30),
		CooldownSeconds:        types.Int64Value(60),
		TerminationPolicy:      types.StringValue("newest_first"),
		Tags:                   tags,
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name != "asg" || req.LaunchTemplateID != "lt-1" {
		t.Errorf("unexpected required fields: %+v", req)
	}
	if req.MinSize != 1 || req.MaxSize != 5 || req.DesiredCapacity != 2 {
		t.Errorf("unexpected sizes: %+v", req)
	}
	if len(req.SubnetIDs) != 1 || req.SubnetIDs[0] != "sn-1" {
		t.Errorf("expected subnet, got %v", req.SubnetIDs)
	}
	if len(req.LoadBalancerPoolIDs) != 1 {
		t.Errorf("expected pool, got %v", req.LoadBalancerPoolIDs)
	}
	if req.HealthCheckType != "lb" {
		t.Errorf("expected healthCheckType lb, got %s", req.HealthCheckType)
	}
	if req.HealthCheckGracePeriod == nil || *req.HealthCheckGracePeriod != 120 {
		t.Error("expected grace period 120")
	}
	if req.WarmupSeconds == nil || *req.WarmupSeconds != 30 {
		t.Error("expected warmup 30")
	}
	if req.CooldownSeconds == nil || *req.CooldownSeconds != 60 {
		t.Error("expected cooldown 60")
	}
	if req.TerminationPolicy != "newest_first" {
		t.Errorf("expected termination policy, got %s", req.TerminationPolicy)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestScaleGroupModelToCreateRequestMinimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	subnets, _ := types.SetValueFrom(ctx, types.StringType, []string{"sn-1"})
	model := ScaleGroupModel{
		Name:                   types.StringValue("asg"),
		LaunchTemplateID:       types.StringValue("lt-1"),
		MinSize:                types.Int64Value(1),
		MaxSize:                types.Int64Value(5),
		DesiredCapacity:        types.Int64Value(2),
		SubnetIDs:              subnets,
		LoadBalancerPoolIDs:    types.SetNull(types.StringType),
		HealthCheckType:        types.StringNull(),
		HealthCheckGracePeriod: types.Int64Null(),
		WarmupSeconds:          types.Int64Null(),
		CooldownSeconds:        types.Int64Null(),
		TerminationPolicy:      types.StringNull(),
		Tags:                   types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.LoadBalancerPoolIDs != nil || req.Tags != nil {
		t.Error("expected nil optional collections")
	}
	if req.HealthCheckGracePeriod != nil || req.WarmupSeconds != nil || req.CooldownSeconds != nil {
		t.Error("expected nil optional int pointers")
	}
	if req.HealthCheckType != "" || req.TerminationPolicy != "" {
		t.Error("expected empty optional strings")
	}
}

func TestScaleGroupModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	planSubnets, _ := types.SetValueFrom(ctx, types.StringType, []string{"sn-2"})
	stateSubnets, _ := types.SetValueFrom(ctx, types.StringType, []string{"sn-1"})

	plan := ScaleGroupModel{
		Name:                   types.StringValue("new"),
		LaunchTemplateID:       types.StringValue("lt-2"),
		MinSize:                types.Int64Value(2),
		MaxSize:                types.Int64Value(10),
		DesiredCapacity:        types.Int64Value(4),
		SubnetIDs:              planSubnets,
		LoadBalancerPoolIDs:    types.SetNull(types.StringType),
		HealthCheckType:        types.StringValue("both"),
		HealthCheckGracePeriod: types.Int64Value(200),
		WarmupSeconds:          types.Int64Value(45),
		CooldownSeconds:        types.Int64Value(90),
		TerminationPolicy:      types.StringValue("newest_first"),
		Tags:                   types.MapNull(types.StringType),
	}
	state := ScaleGroupModel{
		Name:                   types.StringValue("old"),
		LaunchTemplateID:       types.StringValue("lt-1"),
		MinSize:                types.Int64Value(1),
		MaxSize:                types.Int64Value(5),
		DesiredCapacity:        types.Int64Value(2),
		SubnetIDs:              stateSubnets,
		LoadBalancerPoolIDs:    stateSubnets,
		HealthCheckType:        types.StringValue("instance"),
		HealthCheckGracePeriod: types.Int64Value(300),
		WarmupSeconds:          types.Int64Value(0),
		CooldownSeconds:        types.Int64Value(300),
		TerminationPolicy:      types.StringValue("oldest_first"),
		Tags:                   types.MapNull(types.StringType),
	}

	req := plan.toUpdateRequest(ctx, &state, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name == nil || *req.Name != "new" {
		t.Error("expected name update")
	}
	if req.LaunchTemplateID == nil || *req.LaunchTemplateID != "lt-2" {
		t.Error("expected launch template update")
	}
	if req.MinSize == nil || *req.MinSize != 2 {
		t.Error("expected minSize update")
	}
	if req.MaxSize == nil || *req.MaxSize != 10 {
		t.Error("expected maxSize update")
	}
	if req.DesiredCapacity == nil || *req.DesiredCapacity != 4 {
		t.Error("expected desiredCapacity update")
	}
	if len(req.SubnetIDs) != 1 || req.SubnetIDs[0] != "sn-2" {
		t.Errorf("expected subnet update, got %v", req.SubnetIDs)
	}
	// pool changed from set -> null = empty slice
	if req.LoadBalancerPoolIDs == nil || len(req.LoadBalancerPoolIDs) != 0 {
		t.Errorf("expected empty pool slice, got %v", req.LoadBalancerPoolIDs)
	}
	if req.HealthCheckType == nil || *req.HealthCheckType != "both" {
		t.Error("expected healthCheckType update")
	}
	if req.HealthCheckGracePeriod == nil || *req.HealthCheckGracePeriod != 200 {
		t.Error("expected grace period update")
	}
	if req.TerminationPolicy == nil || *req.TerminationPolicy != "newest_first" {
		t.Error("expected termination policy update")
	}
}

func TestScaleGroupModelToUpdateRequestNoChanges(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	subnets, _ := types.SetValueFrom(ctx, types.StringType, []string{"sn-1"})
	same := ScaleGroupModel{
		Name:                   types.StringValue("same"),
		LaunchTemplateID:       types.StringValue("lt-1"),
		MinSize:                types.Int64Value(1),
		MaxSize:                types.Int64Value(5),
		DesiredCapacity:        types.Int64Value(2),
		SubnetIDs:              subnets,
		LoadBalancerPoolIDs:    types.SetNull(types.StringType),
		HealthCheckType:        types.StringValue("instance"),
		HealthCheckGracePeriod: types.Int64Value(300),
		WarmupSeconds:          types.Int64Value(0),
		CooldownSeconds:        types.Int64Value(300),
		TerminationPolicy:      types.StringValue("oldest_first"),
		Tags:                   types.MapNull(types.StringType),
	}

	req := same.toUpdateRequest(ctx, &same, &diags)
	if req.Name != nil || req.LaunchTemplateID != nil || req.MinSize != nil ||
		req.MaxSize != nil || req.DesiredCapacity != nil || req.SubnetIDs != nil ||
		req.HealthCheckType != nil || req.TerminationPolicy != nil || req.Tags != nil {
		t.Errorf("expected empty update request, got %+v", req)
	}
}

func TestScaleGroupModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiScaleGroup{
		ID:                     "asg-1",
		Name:                   "asg",
		LaunchTemplateID:       "lt-1",
		MinSize:                1,
		MaxSize:                5,
		DesiredCapacity:        2,
		CurrentSize:            2,
		Status:                 "active",
		SubnetIDs:              []string{"sn-1"},
		LoadBalancerPoolIDs:    []string{"pool-1"},
		HealthCheckType:        "lb",
		HealthCheckGracePeriod: 120,
		WarmupSeconds:          30,
		CooldownSeconds:        60,
		TerminationPolicy:      "newest_first",
		Tags:                   map[string]string{"env": "prod"},
		CreatedAt:              "2025-01-01T00:00:00Z",
		UpdatedAt:              "2025-01-02T00:00:00Z",
	}

	var model ScaleGroupModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if model.ID.ValueString() != "asg-1" {
		t.Errorf("expected ID asg-1, got %s", model.ID.ValueString())
	}
	if model.CurrentSize.ValueInt64() != 2 {
		t.Errorf("expected currentSize 2, got %d", model.CurrentSize.ValueInt64())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
	if model.HealthCheckType.ValueString() != "lb" {
		t.Errorf("expected healthCheckType lb, got %s", model.HealthCheckType.ValueString())
	}
	if model.HealthCheckGracePeriod.ValueInt64() != 120 {
		t.Errorf("expected grace 120, got %d", model.HealthCheckGracePeriod.ValueInt64())
	}
	if len(model.SubnetIDs.Elements()) != 1 {
		t.Errorf("expected 1 subnet, got %d", len(model.SubnetIDs.Elements()))
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected updatedAt set, got %s", model.UpdatedAt.ValueString())
	}
}

func TestScaleGroupModelFromAPINullsAndZeros(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiScaleGroup{
		ID:               "asg-2",
		Name:             "min",
		LaunchTemplateID: "lt-1",
		MinSize:          1,
		MaxSize:          5,
		DesiredCapacity:  2,
		Status:           "provisioning",
		SubnetIDs:        []string{"sn-1"},
		CreatedAt:        "2025-01-01T00:00:00Z",
	}

	var model ScaleGroupModel
	// pre-populate computed int fields as non-null to exercise the "zero" branches
	model.HealthCheckGracePeriod = types.Int64Value(300)
	model.WarmupSeconds = types.Int64Value(10)
	model.CooldownSeconds = types.Int64Value(300)
	model.LoadBalancerPoolIDs = types.SetNull(types.StringType)
	model.Tags = types.MapNull(types.StringType)

	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updatedAt")
	}
	if !model.HealthCheckType.IsNull() {
		t.Error("expected null healthCheckType")
	}
	// zero values reflected since model fields were non-null
	if model.HealthCheckGracePeriod.ValueInt64() != 0 {
		t.Errorf("expected grace 0, got %d", model.HealthCheckGracePeriod.ValueInt64())
	}
	if model.WarmupSeconds.ValueInt64() != 0 {
		t.Errorf("expected warmup 0, got %d", model.WarmupSeconds.ValueInt64())
	}
	if !model.LoadBalancerPoolIDs.IsNull() {
		t.Error("expected null pools")
	}
	if !model.Tags.IsNull() {
		t.Error("expected null tags")
	}
}

func TestScaleGroupModelFromAPINullIntsStayNull(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiScaleGroup{
		ID:               "asg-3",
		Name:             "min",
		LaunchTemplateID: "lt-1",
		Status:           "active",
		SubnetIDs:        []string{"sn-1"},
		CreatedAt:        "2025-01-01T00:00:00Z",
	}

	var model ScaleGroupModel
	model.HealthCheckGracePeriod = types.Int64Null()
	model.WarmupSeconds = types.Int64Null()
	model.CooldownSeconds = types.Int64Null()
	model.LoadBalancerPoolIDs = types.SetNull(types.StringType)
	model.Tags = types.MapNull(types.StringType)
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if !model.HealthCheckGracePeriod.IsNull() {
		t.Error("expected grace to stay null")
	}
	if !model.WarmupSeconds.IsNull() {
		t.Error("expected warmup to stay null")
	}
	if !model.CooldownSeconds.IsNull() {
		t.Error("expected cooldown to stay null")
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
	if resp.TypeName != "frostmoln_scale_group" {
		t.Errorf("expected frostmoln_scale_group, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "name", "launch_template_id", "min_size", "max_size",
		"desired_capacity", "current_size", "status", "subnet_ids", "load_balancer_pool_ids",
		"health_check_type", "health_check_grace_period", "warmup_seconds", "cooldown_seconds",
		"termination_policy", "tags", "created_at", "updated_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &scaleGroupResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &scaleGroupResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestPollDefaults(t *testing.T) {
	r := &scaleGroupResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 10*time.Minute {
		t.Errorf("expected default timeout 10m, got %v", r.getPollTimeout())
	}
	r2 := &scaleGroupResource{pollInterval: time.Second, pollTimeout: time.Minute}
	if r2.getPollInterval() != time.Second || r2.getPollTimeout() != time.Minute {
		t.Error("expected overridden poll values")
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: raw}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "asg-123"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "asg-123" {
		t.Errorf("expected imported id asg-123, got %s", id.ValueString())
	}
}

// --- tfsdk helpers ---

func buildSGState(t *testing.T, model ScaleGroupModel) tfsdk.State {
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

func buildSGPlan(t *testing.T, model ScaleGroupModel) tfsdk.Plan {
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

func emptySGState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: raw}
}

func fullSGModel(t *testing.T) ScaleGroupModel {
	t.Helper()
	ctx := context.Background()
	subnets, _ := types.SetValueFrom(ctx, types.StringType, []string{"sn-1"})
	return ScaleGroupModel{
		ID:                     types.StringValue("asg-1"),
		Name:                   types.StringValue("asg"),
		LaunchTemplateID:       types.StringValue("lt-1"),
		MinSize:                types.Int64Value(1),
		MaxSize:                types.Int64Value(5),
		DesiredCapacity:        types.Int64Value(2),
		CurrentSize:            types.Int64Value(2),
		Status:                 types.StringValue("active"),
		SubnetIDs:              subnets,
		LoadBalancerPoolIDs:    types.SetNull(types.StringType),
		HealthCheckType:        types.StringValue("instance"),
		HealthCheckGracePeriod: types.Int64Value(300),
		WarmupSeconds:          types.Int64Value(0),
		CooldownSeconds:        types.Int64Value(300),
		TerminationPolicy:      types.StringValue("oldest_first"),
		Tags:                   types.MapNull(types.StringType),
		CreatedAt:              types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:              types.StringNull(),
	}
}

func sgJSON(status string) apiScaleGroup {
	return apiScaleGroup{
		ID:                "asg-1",
		Name:              "asg",
		LaunchTemplateID:  "lt-1",
		MinSize:           1,
		MaxSize:           5,
		DesiredCapacity:   2,
		CurrentSize:       2,
		Status:            status,
		SubnetIDs:         []string{"sn-1"},
		HealthCheckType:   "instance",
		TerminationPolicy: "oldest_first",
		CreatedAt:         "2025-01-01T00:00:00Z",
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/scale-groups":
			var body apiCreateScaleGroupRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name != "asg" {
				t.Errorf("expected name asg, got %s", body.Name)
			}
			// Provisioning returns 202 + an Operation envelope (operationId only).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-sg-1", "status": "pending", "resourceType": "scale_group",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-sg-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-sg-1", "status": "completed", "resourceType": "scale_group", "resourceId": "asg-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			_ = json.NewEncoder(w).Encode(sgJSON("active"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}

	planModel := fullSGModel(t)
	planModel.ID = types.StringNull()
	planModel.Status = types.StringNull()
	plan := buildSGPlan(t, planModel)

	createResp := resource.CreateResponse{State: emptySGState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
	var result ScaleGroupModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "asg-1" {
		t.Errorf("expected ID asg-1, got %s", result.ID.ValueString())
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

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	planModel := fullSGModel(t)
	planModel.ID = types.StringNull()
	planModel.Status = types.StringNull()
	plan := buildSGPlan(t, planModel)

	createResp := resource.CreateResponse{State: emptySGState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error on create API failure")
	}
}

func TestCreatePollError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/scale-groups":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-sg-err", "status": "pending", "resourceType": "scale_group",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-sg-err":
			// The create workflow failed → operation terminal-failed → create errors.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-sg-err", "status": "failed", "resourceType": "scale_group",
				"error": "scale group entered error state",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	planModel := fullSGModel(t)
	planModel.ID = types.StringNull()
	planModel.Status = types.StringNull()
	plan := buildSGPlan(t, planModel)

	createResp := resource.CreateResponse{State: emptySGState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when scale group operation fails")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1" {
			_ = json.NewEncoder(w).Encode(sgJSON("active"))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c}
	state := buildSGState(t, fullSGModel(t))

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result ScaleGroupModel
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

	r := &scaleGroupResource{client: c}
	state := buildSGState(t, fullSGModel(t))

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	var result ScaleGroupModel
	if diags := readResp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestUpdate(t *testing.T) {
	var patchBody apiUpdateScaleGroupRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			_ = json.NewDecoder(r.Body).Decode(&patchBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			out := sgJSON("active")
			out.DesiredCapacity = 4
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	state := buildSGState(t, fullSGModel(t))
	planModel := fullSGModel(t)
	planModel.DesiredCapacity = types.Int64Value(4)
	plan := buildSGPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if patchBody.DesiredCapacity == nil || *patchBody.DesiredCapacity != 4 {
		t.Error("expected desiredCapacity in patch body")
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

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	state := buildSGState(t, fullSGModel(t))
	planModel := fullSGModel(t)
	planModel.DesiredCapacity = types.Int64Value(4)
	plan := buildSGPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error on update API failure")
	}
}

func TestDelete(t *testing.T) {
	deleted := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			if deleted.Load() {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
			} else {
				_ = json.NewEncoder(w).Encode(sgJSON("deleting"))
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	state := buildSGState(t, fullSGModel(t))

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted.Load() {
		t.Error("expected DELETE to be called")
	}
}

func TestReadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c}
	state := buildSGState(t, fullSGModel(t))

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestDeleteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	state := buildSGState(t, fullSGModel(t))

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error on delete server error")
	}
}

func TestDeletePollError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/scale-groups/asg-1":
			// stays in error state -> poll fails
			_ = json.NewEncoder(w).Encode(sgJSON("error"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	state := buildSGState(t, fullSGModel(t))

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete poll reaches error state")
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

	r := &scaleGroupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
	state := buildSGState(t, fullSGModel(t))

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of gone resource should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}
