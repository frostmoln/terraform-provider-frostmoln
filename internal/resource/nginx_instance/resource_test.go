package nginx_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// mustCfgMap builds a types.Map from a Go map for use in test models.
func mustCfgMap(m map[string]string) types.Map {
	v, diags := types.MapValueFrom(context.Background(), types.StringType, m)
	if diags.HasError() {
		panic(diags.Errors())
	}
	return v
}

// --- Model unit tests ---

func TestNginxInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := NginxInstanceModel{
		Name:       types.StringValue("my-nginx"),
		Version:    types.StringValue("1.27"),
		FlavorID:   types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		VPCID:      types.StringValue("vpc-1"),
		SubnetID:   types.StringValue("sn-1"),
		TLSEnabled: types.BoolNull(),
		PHPEnabled: types.BoolNull(),
		PHPVersion: types.StringNull(),
		Config:     types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "nginx" {
		t.Errorf("expected engine nginx, got %s", req.Engine)
	}
	if req.Name != "my-nginx" {
		t.Errorf("expected name my-nginx, got %s", req.Name)
	}
	if req.FlavorID != "web.gp1.small" {
		t.Errorf("expected flavorId web.gp1.small, got %s", req.FlavorID)
	}
	if req.StorageGB != 20 {
		t.Errorf("expected storageGb 20, got %d", req.StorageGB)
	}
	if req.VPCID != "vpc-1" {
		t.Errorf("expected vpcId vpc-1, got %s", req.VPCID)
	}
	if req.SubnetID != "sn-1" {
		t.Errorf("expected subnetId sn-1, got %s", req.SubnetID)
	}
	if req.TLSEnabled != nil {
		t.Error("expected nil tlsEnabled for null value")
	}
	if req.PHPEnabled != nil {
		t.Error("expected nil phpEnabled for null value")
	}
	if req.EngineConfig != nil {
		t.Error("expected nil engineConfig for null config")
	}
}

func TestNginxInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := NginxInstanceModel{
		Name:       types.StringValue("my-nginx"),
		Version:    types.StringValue("1.27"),
		FlavorID:   types.StringValue("web.gp1.medium"),
		StorageGB:  types.Int64Value(40),
		VPCID:      types.StringValue("vpc-1"),
		SubnetID:   types.StringValue("sn-1"),
		TLSEnabled: types.BoolValue(true),
		PHPEnabled: types.BoolValue(true),
		PHPVersion: types.StringValue("8.3"),
		Config:     mustCfgMap(map[string]string{"client_max_body_size": "10m"}),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.TLSEnabled == nil || !*req.TLSEnabled {
		t.Error("expected tlsEnabled true")
	}
	if req.PHPEnabled == nil || !*req.PHPEnabled {
		t.Error("expected phpEnabled true")
	}
	if req.PHPVersion != "8.3" {
		t.Errorf("expected phpVersion 8.3, got %s", req.PHPVersion)
	}
	if req.EngineConfig["client_max_body_size"] != "10m" {
		t.Errorf("expected engineConfig client_max_body_size=10m, got %v", req.EngineConfig)
	}
}

func TestNginxInstanceModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	plan := NginxInstanceModel{
		Name:       types.StringValue("new-name"),
		FlavorID:   types.StringValue("web.gp1.large"),
		StorageGB:  types.Int64Value(80),
		TLSEnabled: types.BoolValue(true),
		PHPEnabled: types.BoolValue(true),
		PHPVersion: types.StringValue("8.3"),
		Config:     mustCfgMap(map[string]string{"gzip": "true"}),
	}
	state := NginxInstanceModel{
		Name:       types.StringValue("old-name"),
		FlavorID:   types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		TLSEnabled: types.BoolValue(false),
		PHPEnabled: types.BoolValue(false),
		PHPVersion: types.StringNull(),
		Config:     types.MapNull(types.StringType),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update")
	}
	if req.TLSEnabled == nil || !*req.TLSEnabled {
		t.Error("expected tlsEnabled update")
	}

	// Engine config is NOT part of the instance PUT (the backend rejects it there with
	// 400); it is reported separately so Update can route it to PUT /:id/config.
	cfg, changed := plan.engineConfigChange(ctx, &state, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if !changed {
		t.Fatal("expected an engine config change")
	}
	if cfg["gzip"] != "true" {
		t.Errorf("expected engineConfig gzip=true, got %v", cfg)
	}
}

// TestNginxInstanceModelEngineConfigChange covers the three plan shapes that decide
// whether an engine config apply runs at all: an explicit empty map is a real change
// (reset to boot defaults), while null (attribute removed) and unknown are not.
func TestNginxInstanceModelEngineConfigChange(t *testing.T) {
	ctx := context.Background()
	state := NginxInstanceModel{Config: mustCfgMap(map[string]string{"gzip": "true"})}

	t.Run("empty map is a reset", func(t *testing.T) {
		diags := diag.Diagnostics{}
		plan := NginxInstanceModel{Config: mustCfgMap(map[string]string{})}
		cfg, changed := plan.engineConfigChange(ctx, &state, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		if !changed || len(cfg) != 0 {
			t.Errorf("expected an empty-map change, got changed=%v cfg=%v", changed, cfg)
		}
	})

	for name, planCfg := range map[string]types.Map{
		"null is no change":    types.MapNull(types.StringType),
		"unknown is no change": types.MapUnknown(types.StringType),
	} {
		t.Run(name, func(t *testing.T) {
			diags := diag.Diagnostics{}
			plan := NginxInstanceModel{Config: planCfg}
			if _, changed := plan.engineConfigChange(ctx, &state, &diags); changed {
				t.Error("expected no engine config change")
			}
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags.Errors())
			}
		})
	}
}

func TestNginxInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	same := NginxInstanceModel{
		Name:       types.StringValue("same"),
		FlavorID:   types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		TLSEnabled: types.BoolValue(true),
		PHPEnabled: types.BoolValue(false),
		PHPVersion: types.StringValue("8.2"),
		Config:     mustCfgMap(map[string]string{"gzip": "true"}),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil || req.TLSEnabled != nil {
		t.Error("expected no changes in update request")
	}
	if _, changed := same.engineConfigChange(ctx, &same, &diags); changed {
		t.Error("expected no engine config change")
	}
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
}

func TestNginxInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiWebserverInstance{
		ID:            "nginx-123",
		Name:          "my-nginx",
		Engine:        "nginx",
		EngineVersion: "1.27",
		FlavorID:      "web.gp1.small",
		StorageGB:     20,
		VPCID:         "vpc-1",
		SubnetID:      "sn-1",
		TLSEnabled:    true,
		PHPEnabled:    true,
		PHPVersion:    "8.3",
		EngineConfig:  map[string]string{"client_max_body_size": "10m"},
		Status:        "running",
		PrivateIP:     "10.0.1.5",
		Port:          443,
		CreatedAt:     "2025-01-01T00:00:00Z",
		UpdatedAt:     "2025-01-02T00:00:00Z",
		TenantID:      "t-1",
	}

	var model NginxInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "nginx-123" {
		t.Errorf("expected ID nginx-123, got %s", model.ID.ValueString())
	}
	if model.FlavorID.ValueString() != "web.gp1.small" {
		t.Errorf("expected flavor web.gp1.small, got %s", model.FlavorID.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-1" {
		t.Errorf("expected vpc_id vpc-1, got %s", model.VPCID.ValueString())
	}
	if model.SubnetID.ValueString() != "sn-1" {
		t.Errorf("expected subnet_id sn-1, got %s", model.SubnetID.ValueString())
	}
	if model.PHPVersion.ValueString() != "8.3" {
		t.Errorf("expected php_version 8.3, got %s", model.PHPVersion.ValueString())
	}
	cfg := map[string]string{}
	model.Config.ElementsAs(ctx, &cfg, false)
	if cfg["client_max_body_size"] != "10m" {
		t.Errorf("expected config client_max_body_size=10m, got %v", cfg)
	}
	if model.Port.ValueInt64() != 443 {
		t.Errorf("expected port 443, got %d", model.Port.ValueInt64())
	}
	if model.TenantID.ValueString() != "t-1" {
		t.Errorf("expected tenant_id t-1, got %s", model.TenantID.ValueString())
	}
}

func TestNginxInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiWebserverInstance{
		ID:            "nginx-123",
		Name:          "my-nginx",
		Engine:        "nginx",
		EngineVersion: "1.27",
		FlavorID:      "web.gp1.small",
		StorageGB:     20,
		VPCID:         "vpc-1",
		SubnetID:      "sn-1",
		TLSEnabled:    false,
		PHPEnabled:    false,
		Status:        "provisioning",
		CreatedAt:     "2025-01-01T00:00:00Z",
	}

	var model NginxInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.PHPVersion.IsNull() {
		t.Error("expected null php_version")
	}
	if !model.Config.IsNull() {
		t.Error("expected null config")
	}
	if !model.PrivateIP.IsNull() {
		t.Error("expected null private_ip")
	}
	if !model.Port.IsNull() {
		t.Error("expected null port")
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
	if !model.TenantID.IsNull() {
		t.Error("expected null tenant_id")
	}
}

// --- Resource unit tests ---

func TestNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_nginx_instance" {
		t.Errorf("expected type name frostmoln_nginx_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "version", "flavor_id", "storage_gb", "vpc_id", "subnet_id"}
	for _, attr := range requiredAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	for _, attr := range []string{"php_enabled", "php_version", "config"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	computedAttrs := []string{"id", "status", "private_ip", "port", "created_at", "updated_at", "tenant_id"}
	for _, attr := range computedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}
}

// PHP is installed at boot (php-fpm apt install) and the webserver API rejects a PHP
// change on PUT with 400, so both attributes must force a replacement. Both cases are
// behavioural, because the ORDER of the plan modifiers decides the outcome: they are
// Optional+Computed, so the framework marks them unknown on any update where config
// omits them, and RequiresReplace-before-UseStateForUnknown would then compare unknown
// against the state value and replace a running instance on every unrelated change.
func TestPHPAttributesReplaceOnlyOnRealChange(t *testing.T) {
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	phpEnabled, ok := schemaResp.Schema.Attributes["php_enabled"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("php_enabled is not a BoolAttribute")
	}
	phpVersion, ok := schemaResp.Schema.Attributes["php_version"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("php_version is not a StringAttribute")
	}

	stateModel := NginxInstanceModel{
		ID:         types.StringValue("ws-1"),
		Name:       types.StringValue("site-1"),
		PHPEnabled: types.BoolValue(true),
		PHPVersion: types.StringValue("8.3"),
		Config:     types.MapNull(types.StringType),
	}
	state := buildNginxInstanceState(t, stateModel)
	planModel := stateModel
	planModel.Name = types.StringValue("site-renamed")
	plan := buildNginxInstancePlan(t, planModel)

	// A real change replaces.
	if !runBoolModifiers(t, phpEnabled.PlanModifiers, state, plan, types.BoolValue(true), types.BoolValue(false)) {
		t.Error("php_enabled true->false must require replacement")
	}
	if !runStringModifiers(t, phpVersion.PlanModifiers, state, plan, types.StringValue("8.3"), types.StringValue("8.2")) {
		t.Error("php_version 8.3->8.2 must require replacement")
	}

	// An unrelated update with PHP omitted from config (framework marks it unknown)
	// must NOT replace — this is what a wrong modifier order destroys.
	if runBoolModifiers(t, phpEnabled.PlanModifiers, state, plan, types.BoolValue(true), types.BoolUnknown()) {
		t.Error("php_enabled must not require replacement when it is unknown-from-computed and unchanged in state")
	}
	if runStringModifiers(t, phpVersion.PlanModifiers, state, plan, types.StringValue("8.3"), types.StringUnknown()) {
		t.Error("php_version must not require replacement when it is unknown-from-computed and unchanged in state")
	}
}

// runBoolModifiers / runStringModifiers mirror how the framework chains attribute plan
// modifiers (internal/fwserver/attribute_plan_modification.go): each modifier sees the
// previous one's PlanValue, and RequiresReplace, once set, is never unset.
func runBoolModifiers(t *testing.T, mods []planmodifier.Bool, state tfsdk.State, plan tfsdk.Plan, stateValue, planValue types.Bool) bool {
	t.Helper()
	req := planmodifier.BoolRequest{
		Path:        path.Root("php_enabled"),
		State:       state,
		Plan:        plan,
		StateValue:  stateValue,
		PlanValue:   planValue,
		ConfigValue: types.BoolNull(),
	}
	replace := false
	for _, m := range mods {
		resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
		m.PlanModifyBool(context.Background(), req, resp)
		req.PlanValue = resp.PlanValue
		replace = replace || resp.RequiresReplace
	}
	return replace
}

func runStringModifiers(t *testing.T, mods []planmodifier.String, state tfsdk.State, plan tfsdk.Plan, stateValue, planValue types.String) bool {
	t.Helper()
	req := planmodifier.StringRequest{
		Path:        path.Root("php_version"),
		State:       state,
		Plan:        plan,
		StateValue:  stateValue,
		PlanValue:   planValue,
		ConfigValue: types.StringNull(),
	}
	replace := false
	for _, m := range mods {
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		m.PlanModifyString(context.Background(), req, resp)
		req.PlanValue = resp.PlanValue
		replace = replace || resp.RequiresReplace
	}
	return replace
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &nginxInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &nginxInstanceResource{}
	req := resource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &nginxInstanceResource{}
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	req := resource.ConfigureRequest{ProviderData: c}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for valid client, got %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

// --- state/plan helpers ---

func buildNginxInstanceState(t *testing.T, model NginxInstanceModel) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{Schema: schemaResp.Schema}
	diags := state.Set(context.Background(), &model)
	if diags.HasError() {
		t.Fatalf("failed to set state: %v", diags.Errors())
	}
	return state
}

func buildNginxInstancePlan(t *testing.T, model NginxInstanceModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	diags := plan.Set(context.Background(), &model)
	if diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

func emptyNginxInstanceState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func newTestNginxResource(c *client.Client) *nginxInstanceResource {
	return &nginxInstanceResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

func baseNginxModel() NginxInstanceModel {
	return NginxInstanceModel{
		Name:       types.StringValue("my-nginx"),
		Version:    types.StringValue("1.27"),
		FlavorID:   types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		VPCID:      types.StringValue("vpc-1"),
		SubnetID:   types.StringValue("sn-1"),
		TLSEnabled: types.BoolValue(true),
		PHPEnabled: types.BoolValue(false),
		PHPVersion: types.StringNull(),
		Config:     mustCfgMap(map[string]string{"client_max_body_size": "10m"}),
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers":
			var body apiCreateWebserverInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Engine != "nginx" {
				t.Errorf("expected engine nginx, got %s", body.Engine)
			}
			if body.FlavorID != "web.gp1.small" {
				t.Errorf("expected flavorId web.gp1.small, got %s", body.FlavorID)
			}
			if body.VPCID != "vpc-1" {
				t.Errorf("expected vpcId vpc-1, got %s", body.VPCID)
			}
			if body.SubnetID != "sn-1" {
				t.Errorf("expected subnetId sn-1, got %s", body.SubnetID)
			}
			if body.EngineConfig["client_max_body_size"] != "10m" {
				t.Errorf("expected engineConfig object client_max_body_size=10m, got %v", body.EngineConfig)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-new",
				Name:          body.Name,
				Engine:        "nginx",
				EngineVersion: body.EngineVersion,
				FlavorID:      body.FlavorID,
				StorageGB:     body.StorageGB,
				VPCID:         body.VPCID,
				SubnetID:      body.SubnetID,
				EngineConfig:  body.EngineConfig,
				Status:        "provisioning",
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-new",
				Name:          "test-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				FlavorID:      "web.gp1.small",
				StorageGB:     20,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				TLSEnabled:    true,
				EngineConfig:  map[string]string{"client_max_body_size": "10m"},
				Status:        status,
				PrivateIP:     "10.0.1.5",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
				TenantID:      "t-1",
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.Name = types.StringValue("test-nginx")
	plan := buildNginxInstancePlan(t, model)

	createResp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "nginx-new" {
		t.Errorf("expected ID nginx-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 443 {
		t.Errorf("expected port 443, got %d", result.Port.ValueInt64())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	createResp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildNginxInstancePlan(t, baseNginxModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID: "nginx-err", Name: "x", Engine: "nginx", EngineVersion: "1.27",
				FlavorID: "f", StorageGB: 20, VPCID: "vpc-1", SubnetID: "sn-1",
				Status: "provisioning", CreatedAt: "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-err":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID: "nginx-err", Name: "x", Engine: "nginx", EngineVersion: "1.27",
				FlavorID: "f", StorageGB: 20, VPCID: "vpc-1", SubnetID: "sn-1",
				Status: "failed", CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	createResp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildNginxInstancePlan(t, baseNginxModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters failed state during polling")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123" {
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-123",
				Name:          "my-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				FlavorID:      "web.gp1.small",
				StorageGB:     20,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				TLSEnabled:    true,
				EngineConfig:  map[string]string{"client_max_body_size": "10m"},
				Status:        "running",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &nginxInstanceResource{client: c}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	cfg := map[string]string{}
	result.Config.ElementsAs(context.Background(), &cfg, false)
	if cfg["client_max_body_size"] != "10m" {
		t.Errorf("expected config client_max_body_size=10m, got %v", cfg)
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &nginxInstanceResource{client: c}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-gone")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	diags := readResp.State.Get(context.Background(), &result)
	if !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state to be removed after 404")
		}
	}
}

func TestReadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &nginxInstanceResource{client: c}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdateWebserverInstanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-123",
				Name:          "updated-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				FlavorID:      "web.gp1.small",
				StorageGB:     20,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				TLSEnabled:    true,
				Status:        "running",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Name = types.StringValue("old-nginx")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	// An in-place field change (name) goes through PUT; flavor_id and storage_gb
	// are held constant (flavor changes are plan-rejected, storage goes via
	// /resize) and must NOT appear in the PUT body.
	planModel := stateModel
	planModel.Name = types.StringValue("updated-nginx")
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if updatedBody.Name == nil || *updatedBody.Name != "updated-nginx" {
		t.Error("expected name in update request")
	}
}

// TestUpdateConfigUsesConfigRoute verifies a `config` change is routed to
// PUT /webservers/{id}/config — never to the instance PUT, which rejects engineConfig
// with 400 — and that the provider waits for the async apply to reach "applied".
func TestUpdateConfigUsesConfigRoute(t *testing.T) {
	var configBody string
	var instancePutCalled bool
	var configGets int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			b, _ := io.ReadAll(r.Body)
			configBody = string(b)
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"engineConfig":{"gzip":"true"},"configVersion":4,"configStatus":"applying"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			// Still applying on the first poll, terminal on the next.
			if atomic.AddInt32(&configGets, 1) == 1 {
				_, _ = fmt.Fprint(w, `{"engineConfig":{"gzip":"true"},"configVersion":4,"configStatus":"applying"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"engineConfig":{"gzip":"true"},"configVersion":4,"configStatus":"applied"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			instancePutCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_, _ = fmt.Fprint(w, `{"id":"nginx-123","name":"my-nginx","engine":"nginx","engineVersion":"1.27",`+
				`"flavorId":"web.gp1.small","storageGb":20,"vpcId":"vpc-1","subnetId":"sn-1","tlsEnabled":true,`+
				`"engineConfig":{"gzip":"true"},"status":"running","port":443,"createdAt":"2025-01-01T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Config = mustCfgMap(map[string]string{"gzip": "true"})
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if instancePutCalled {
		t.Error("engineConfig must not be PUT on the instance route (the backend rejects it with 400)")
	}
	if configBody != `{"engineConfig":{"gzip":"true"}}` {
		t.Errorf("unexpected config body: %s", configBody)
	}
	if configGets < 2 {
		t.Errorf("expected the provider to poll until the apply left \"applying\", got %d GETs", configGets)
	}
}

// TestUpdateConfigEmptyMapResets verifies an explicitly empty `config` reaches the API as
// a present (not omitted) empty object — the reset-to-boot-defaults contract. Omitting it
// would make the reset a silent server-side no-op.
func TestUpdateConfigEmptyMapResets(t *testing.T) {
	var configBody string
	var configGets int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			b, _ := io.ReadAll(r.Body)
			configBody = string(b)
			// The service ALWAYS acks "applying" (it starts the apply saga), so the reset
			// path polls to a terminal state exactly like any other config change.
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"engineConfig":{},"configVersion":9,"configStatus":"applying"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			atomic.AddInt32(&configGets, 1)
			_, _ = fmt.Fprint(w, `{"engineConfig":{},"configVersion":9,"configStatus":"applied"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			// An instance with an empty stored config OMITS engineConfig entirely.
			_, _ = fmt.Fprint(w, `{"id":"nginx-123","name":"my-nginx","engine":"nginx","engineVersion":"1.27",`+
				`"flavorId":"web.gp1.small","storageGb":20,"vpcId":"vpc-1","subnetId":"sn-1","tlsEnabled":true,`+
				`"status":"running","port":443,"createdAt":"2025-01-01T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Config = mustCfgMap(map[string]string{})
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if configBody != `{"engineConfig":{}}` {
		t.Errorf("expected an explicit empty engineConfig object, got: %s", configBody)
	}
	if configGets == 0 {
		t.Error("expected the reset to poll the config route to a terminal state")
	}

	// The read-back omits engineConfig; the configured empty map must survive, otherwise
	// `config = {}` diffs forever.
	var saved NginxInstanceModel
	if diags := updateResp.State.Get(context.Background(), &saved); diags.HasError() {
		t.Fatalf("failed to read state: %v", diags.Errors())
	}
	if saved.Config.IsNull() || len(saved.Config.Elements()) != 0 {
		t.Errorf("expected an empty (not null) config in state, got %v", saved.Config)
	}
}

// TestUpdateConfigSupersededByAnotherClient verifies the poller does not adopt a NEWER
// revision's terminal state as its own outcome: if the portal, the fm CLI or a second
// workspace applies a different config mid-flight, the apply must fail closed rather than
// report someone else's result as success.
func TestUpdateConfigSupersededByAnotherClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"engineConfig":{"gzip":"true"},"configVersion":4,"configStatus":"applying"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			// Someone else's revision 5 landed and applied cleanly.
			_, _ = fmt.Fprint(w, `{"engineConfig":{"spaFallback":"true"},"configVersion":5,"configStatus":"applied"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_, _ = fmt.Fprint(w, `{"id":"nginx-123","name":"my-nginx","engine":"nginx","engineVersion":"1.27",`+
				`"flavorId":"web.gp1.small","storageGb":20,"vpcId":"vpc-1","subnetId":"sn-1","tlsEnabled":true,`+
				`"engineConfig":{"spaFallback":"true"},"status":"running","port":443,"createdAt":"2025-01-01T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Config = mustCfgMap(map[string]string{"gzip": "true"})
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected the update to fail when another client superseded the revision")
	}
	if !strings.Contains(updateResp.Diagnostics.Errors()[0].Detail(), "superseded by revision 5") {
		t.Errorf("expected a superseded-revision diagnostic, got: %s",
			updateResp.Diagnostics.Errors()[0].Detail())
	}
}

// TestUpdateConfigApplyFailed verifies a failed runtime apply fails the Terraform apply
// and surfaces the engine's own rejection reason rather than reporting success.
func TestUpdateConfigApplyFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"engineConfig":{"gzip":"true"},"configVersion":7,"configStatus":"applying"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/config":
			_, _ = fmt.Fprint(w, `{"engineConfig":{"gzip":"true"},"configVersion":7,"configStatus":"failed",`+
				`"configError":"nginx: [emerg] unknown directive"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			// A rename that landed BEFORE the config apply failed.
			_, _ = fmt.Fprint(w, `{"id":"nginx-123","name":"renamed-nginx","engine":"nginx","engineVersion":"1.27",`+
				`"flavorId":"web.gp1.small","storageGb":20,"vpcId":"vpc-1","subnetId":"sn-1","tlsEnabled":true,`+
				`"status":"running","port":443,"createdAt":"2025-01-01T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Config = mustCfgMap(map[string]string{"gzip": "true"})
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected the update to fail when the engine rejects the config")
	}
	if !strings.Contains(updateResp.Diagnostics.Errors()[0].Detail(), "unknown directive") {
		t.Errorf("expected the engine's rejection reason in the diagnostic, got: %s",
			updateResp.Diagnostics.Errors()[0].Detail())
	}

	// Mutations that already landed (here: the rename) must be recorded even though the
	// update failed, otherwise a later `-refresh=false` apply re-proposes them.
	var saved NginxInstanceModel
	if diags := updateResp.State.Get(context.Background(), &saved); diags.HasError() {
		t.Fatalf("failed to read state: %v", diags.Errors())
	}
	if saved.Name.ValueString() != "renamed-nginx" {
		t.Errorf("expected the pre-failure state to be persisted, got name %q", saved.Name.ValueString())
	}
}

// TestUpdateStorageResize verifies a storage_gb grow is routed to POST /resize
// (not the PUT that the backend silently drops storageGb from) and that no PUT
// is issued for a storage-only change.
func TestUpdateStorageResize(t *testing.T) {
	var resizeBody apiResizeWebserverInstanceRequest
	var resizeCalled, putCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123/resize":
			resizeCalled = true
			_ = json.NewDecoder(r.Body).Decode(&resizeBody)
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"status":"resizing"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			putCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-123",
				Name:          "my-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				FlavorID:      "web.gp1.small",
				StorageGB:     80,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				TLSEnabled:    true,
				Status:        "running",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.StorageGB = types.Int64Value(80)
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("resize update failed: %v", updateResp.Diagnostics.Errors())
	}
	if !resizeCalled {
		t.Error("expected POST /resize to be called")
	}
	if resizeBody.StorageGB != 80 {
		t.Errorf("expected resize storageGb=80, got %d", resizeBody.StorageGB)
	}
	if putCalled {
		t.Error("expected no PUT for a storage-only resize")
	}
}

// TestUpdateStorageShrinkRejected verifies the apply-time grow-only backstop:
// a shrink that slips past the plan-time modifier is rejected in Update.
func TestUpdateStorageShrinkRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request on shrink: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.StorageGB = types.Int64Value(10) // shrink from 20
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error when shrinking storage_gb")
	}
}

func TestUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Name = types.StringValue("new")
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on update")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiWebserverInstance{ID: "nginx-123", Status: "deleting"})
			}
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-gone")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone resource should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeletePollError(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &nginxInstanceResource{client: c, pollInterval: 5 * time.Millisecond, pollTimeout: 60 * time.Millisecond}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleted {
		t.Error("expected DELETE to be called")
	}
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete poll keeps failing")
	}
}

func TestPollDefaults(t *testing.T) {
	r := &nginxInstanceResource{}
	if got := r.getPollInterval(); got != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", got)
	}
	if got := r.getPollTimeout(); got != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", got)
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	importResp := resource.ImportStateResponse{State: emptyNginxInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "nginx-123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	importResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "nginx-123" {
		t.Errorf("expected imported ID nginx-123, got %s", result.ID.ValueString())
	}
}

// TestUpgradeState_V0ToV1 guards the v0->v1 migration: a v0 state row carries
// the old `flavor` attribute; the upgrader must copy it into `flavor_id` and
// carry the other attributes through, so the first post-upgrade plan is clean
// (no spurious update, no destroy). Other attributes are null-filled here --
// only the rename behaviour is under test.
func TestUpgradeState_V0ToV1(t *testing.T) {
	ctx := context.Background()
	r := &nginxInstanceResource{}

	up, ok := r.UpgradeState(ctx)[0]
	if !ok {
		t.Fatal("expected a v0 state upgrader")
	}
	if up.PriorSchema == nil {
		t.Fatal("expected PriorSchema for v0")
	}
	if _, ok := up.PriorSchema.Attributes["flavor"]; !ok {
		t.Error("prior schema must carry the old `flavor` attribute")
	}
	if _, ok := up.PriorSchema.Attributes["flavor_id"]; ok {
		t.Error("prior schema must not carry the new `flavor_id` attribute")
	}

	priorType := up.PriorSchema.Type().TerraformType(ctx)
	raw := map[string]tftypes.Value{}
	for name, at := range priorType.(tftypes.Object).AttributeTypes {
		raw[name] = tftypes.NewValue(at, nil)
	}
	raw["id"] = tftypes.NewValue(tftypes.String, "inst-123")
	raw["name"] = tftypes.NewValue(tftypes.String, "my-inst")
	raw["flavor"] = tftypes.NewValue(tftypes.String, "web.gp1.small")
	priorVal := tftypes.NewValue(priorType, raw)

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	req := resource.UpgradeStateRequest{State: &tfsdk.State{Schema: *up.PriorSchema, Raw: priorVal}}
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	up.StateUpgrader(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var model NginxInstanceModel
	resp.State.Get(ctx, &model)
	if model.FlavorID.ValueString() != "web.gp1.small" {
		t.Errorf("expected flavor_id web.gp1.small, got %s", model.FlavorID.ValueString())
	}
	if model.ID.ValueString() != "inst-123" {
		t.Errorf("expected id carried through, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-inst" {
		t.Errorf("expected name carried through, got %s", model.Name.ValueString())
	}
}
