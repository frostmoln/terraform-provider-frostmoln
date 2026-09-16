package messaging_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// TestTimeoutsDefaultsMatchTodaysConstants pins the defaults contract of the
// customer-tunable timeouts block: an absent block resolves to exactly the
// values this resource defaults to — 30m per verb since audit D8 (the
// platform's CategoryLongRunning saga envelope; its readiness step alone is
// budgeted 20m; 15m before) — and the
// pollTimeout test-injection seam still shrinks every budget that does not
// carry an explicit override.
func TestTimeoutsDefaultsMatchTodaysConstants(t *testing.T) {
	r := &messagingInstanceResource{}
	want := timeouts.Uniform(30 * time.Minute)
	if got := r.resolveBudgets(nil); got != want {
		t.Fatalf("an absent timeouts block must keep the 30m default; got %+v, want %+v", got, want)
	}

	r.pollTimeout = 250 * time.Millisecond
	wantInj := timeouts.Uniform(250 * time.Millisecond)
	if got := r.resolveBudgets(nil); got != wantInj {
		t.Fatalf("a shrunken pollTimeout must shrink the default budget; got %+v, want %+v", got, wantInj)
	}
}

// --- Model unit tests ---

func TestMessagingInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MessagingInstanceModel{
		Name:            types.StringValue("my-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-123"),
		SubnetID:        types.StringValue("subnet-456"),
		PersistenceMode: types.StringNull(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Type != "lavinmq" {
		t.Errorf("expected type lavinmq, got %s", req.Type)
	}
	if req.Name != "my-broker" {
		t.Errorf("expected name my-broker, got %s", req.Name)
	}
	if req.TypeVersion != "2.3" {
		t.Errorf("expected typeVersion 2.3, got %s", req.TypeVersion)
	}
	if req.FlavorID != "mq.gp1.small" {
		t.Errorf("expected flavorId mq.gp1.small, got %s", req.FlavorID)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected vpcId vpc-123, got %s", req.VPCID)
	}
	if req.SubnetID != "subnet-456" {
		t.Errorf("expected subnetId subnet-456, got %s", req.SubnetID)
	}
	if req.PersistenceMode != "" {
		t.Errorf("expected empty persistenceMode for null, got %s", req.PersistenceMode)
	}
}

func TestMessagingInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MessagingInstanceModel{
		Name:            types.StringValue("my-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.medium"),
		VPCID:           types.StringValue("vpc-123"),
		SubnetID:        types.StringValue("subnet-456"),
		PersistenceMode: types.StringValue("persistent"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Type != "lavinmq" {
		t.Errorf("expected type lavinmq, got %s", req.Type)
	}
	if req.PersistenceMode != "persistent" {
		t.Errorf("expected persistenceMode persistent, got %s", req.PersistenceMode)
	}
}

func TestMessagingInstanceModelToUpdateRequest(t *testing.T) {
	plan := MessagingInstanceModel{
		Name:            types.StringValue("new-name"),
		FlavorID:        types.StringValue("mq.gp1.large"),
		PersistenceMode: types.StringValue("none"),
	}
	state := MessagingInstanceModel{
		Name:            types.StringValue("old-name"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		PersistenceMode: types.StringValue("persistent"),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update to new-name")
	}
	if req.PersistenceMode == nil || *req.PersistenceMode != "none" {
		t.Error("expected persistenceMode update to none")
	}
	// flavor_id is not part of the PUT update request: flavor changes are
	// refused in Update (flavor resize not yet supported).
}

func TestMessagingInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	same := MessagingInstanceModel{
		Name:            types.StringValue("same"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		PersistenceMode: types.StringValue("persistent"),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil || req.PersistenceMode != nil {
		t.Error("expected no changes in update request")
	}
}

func TestMessagingInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMessagingInstance{
		ID:              "mq-123",
		Name:            "my-broker",
		Type:            "lavinmq",
		TypeVersion:     "2.3",
		FlavorID:        "mq.gp1.small",
		VPCID:           "vpc-123",
		SubnetID:        "subnet-456",
		PersistenceMode: "persistent",
		Status:          "running",
		PrivateIP:       "10.0.1.5",
		Port:            5672,
		AMQPSPort:       5671,
		ManagementPort:  15672,
		CreatedAt:       "2025-01-01T00:00:00Z",
		UpdatedAt:       "2025-01-02T00:00:00Z",
	}

	var model MessagingInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "mq-123" {
		t.Errorf("expected ID mq-123, got %s", model.ID.ValueString())
	}
	if model.Type.ValueString() != "lavinmq" {
		t.Errorf("expected type lavinmq, got %s", model.Type.ValueString())
	}
	if model.Port.ValueInt64() != 5672 {
		t.Errorf("expected port 5672, got %d", model.Port.ValueInt64())
	}
	if model.AMQPSPort.ValueInt64() != 5671 {
		t.Errorf("expected amqps_port 5671, got %d", model.AMQPSPort.ValueInt64())
	}
	if model.ManagementPort.ValueInt64() != 15672 {
		t.Errorf("expected management_port 15672, got %d", model.ManagementPort.ValueInt64())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", model.Status.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected updated_at set, got %s", model.UpdatedAt.ValueString())
	}
}

func TestMessagingInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMessagingInstance{
		ID:              "mq-123",
		Name:            "my-broker",
		Type:            "lavinmq",
		TypeVersion:     "2.3",
		FlavorID:        "mq.gp1.small",
		VPCID:           "vpc-123",
		SubnetID:        "subnet-456",
		PersistenceMode: "persistent",
		Status:          "creating",
		CreatedAt:       "2025-01-01T00:00:00Z",
	}

	var model MessagingInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.PrivateIP.IsNull() {
		t.Error("expected null private_ip")
	}
	if !model.Port.IsNull() {
		t.Error("expected null port")
	}
	if !model.AMQPSPort.IsNull() {
		t.Error("expected null amqps_port")
	}
	if !model.ManagementPort.IsNull() {
		t.Error("expected null management_port")
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
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
	if resp.TypeName != "frostmoln_messaging_instance" {
		t.Errorf("expected type name frostmoln_messaging_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "flavor_id", "vpc_id", "subnet_id"}
	for _, attr := range requiredAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	computedAttrs := []string{"id", "status", "private_ip", "port", "amqps_port", "management_port", "created_at", "updated_at"}
	for _, attr := range computedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}

	// eviction_policy must NOT be present (messaging has no eviction).
	if _, ok := resp.Schema.Attributes["eviction_policy"]; ok {
		t.Error("did not expect eviction_policy attribute in messaging schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &messagingInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &messagingInstanceResource{}
	req := resource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &messagingInstanceResource{}
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

func buildMessagingInstanceState(t *testing.T, model MessagingInstanceModel) tfsdk.State {
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

func buildMessagingInstancePlan(t *testing.T, model MessagingInstanceModel) tfsdk.Plan {
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

func emptyMessagingInstanceState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func newTestMessagingResource(c *client.Client) *messagingInstanceResource {
	return &messagingInstanceResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

// --- CRUD tests ---

// TestCreate covers the async path: messaging create routes through
// provisioning, which returns 202 + an Operation envelope (operationId only).
// The provider polls GET /v1/tenants/{tid}/operations/{id} to completion, takes the
// operation's resourceId as the instance id, then reads the instance.
func TestCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/messaging":
			var body apiCreateMessagingInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Type != "lavinmq" {
				t.Errorf("expected type lavinmq, got %s", body.Type)
			}
			// Provisioning returns 202 + an Operation envelope (operationId only).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-mq-1", "status": "pending", "resourceType": "messaging_instance",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-mq-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-mq-1", "status": "completed", "resourceType": "messaging_instance", "resourceId": "mq-new",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/messaging/mq-new":
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-new",
				Name:            "test-broker",
				Type:            "lavinmq",
				TypeVersion:     "2.3",
				FlavorID:        "mq.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "persistent",
				Status:          "running",
				PrivateIP:       "10.0.1.5",
				Port:            5672,
				AMQPSPort:       5671,
				ManagementPort:  15672,
				CreatedAt:       "2025-01-01T00:00:00Z",
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

	r := newTestMessagingResource(c)

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("test-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})

	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result MessagingInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "mq-new" {
		t.Errorf("expected ID mq-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 5672 {
		t.Errorf("expected port 5672, got %d", result.Port.ValueInt64())
	}
	if result.AMQPSPort.ValueInt64() != 5671 {
		t.Errorf("expected amqps_port 5671, got %d", result.AMQPSPort.ValueInt64())
	}
	if result.ManagementPort.ValueInt64() != 15672 {
		t.Errorf("expected management_port 15672, got %d", result.ManagementPort.ValueInt64())
	}
}

// TestCreateLegacy201 covers the synchronous fallback: a backend that returns
// 201 + the instance body directly (no operation envelope). The provider reads
// the id from the body, then reads the instance to populate final state.
func TestCreateLegacy201(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/messaging":
			var body apiCreateMessagingInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-legacy",
				Name:            body.Name,
				Type:            body.Type,
				TypeVersion:     body.TypeVersion,
				FlavorID:        body.FlavorID,
				VPCID:           body.VPCID,
				SubnetID:        body.SubnetID,
				PersistenceMode: "persistent",
				Status:          "running",
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/messaging/mq-legacy":
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-legacy",
				Name:            "test-broker",
				Type:            "lavinmq",
				TypeVersion:     "2.3",
				FlavorID:        "mq.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "persistent",
				Status:          "running",
				PrivateIP:       "10.0.1.5",
				Port:            5672,
				AMQPSPort:       5671,
				ManagementPort:  15672,
				CreatedAt:       "2025-01-01T00:00:00Z",
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

	r := newTestMessagingResource(c)

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("test-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})

	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result MessagingInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "mq-legacy" {
		t.Errorf("expected ID mq-legacy, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 5672 {
		t.Errorf("expected port 5672, got %d", result.Port.ValueInt64())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/messaging" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestMessagingResource(c)

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("test-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})

	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

// TestCreateOperationFailed covers the async path where the provisioning
// workflow fails: the operation reaches a terminal "failed" state, so Create
// must surface an error rather than writing half-formed state.
func TestCreateOperationFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/messaging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-mq-err", "status": "pending", "resourceType": "messaging_instance",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-mq-err":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-mq-err", "status": "failed", "resourceType": "messaging_instance",
				"error": "messaging instance entered error state",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestMessagingResource(c)

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("x"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("persistent"),
	})

	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when the create operation fails")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123" {
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-123",
				Name:            "my-broker",
				Type:            "lavinmq",
				TypeVersion:     "2.3",
				FlavorID:        "mq.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "persistent",
				Status:          "running",
				Port:            5672,
				AMQPSPort:       5671,
				ManagementPort:  15672,
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &messagingInstanceResource{client: c}

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("my-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result MessagingInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
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

	r := &messagingInstanceResource{client: c}

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-gone"),
		Name:            types.StringValue("gone"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}

	var result MessagingInstanceModel
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

	r := &messagingInstanceResource{client: c}

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("x"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdateMessagingInstanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123":
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID:              "mq-123",
				Name:            "updated-broker",
				Type:            "lavinmq",
				TypeVersion:     "2.3",
				FlavorID:        "mq.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "none",
				Status:          "running",
				Port:            5672,
				CreatedAt:       "2025-01-01T00:00:00Z",
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

	r := newTestMessagingResource(c)

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("old-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	// Rename + persistence-mode change. flavor_id is unchanged (a flavor change
	// is refused in Update, so Update never receives one).
	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("updated-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("none"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if updatedBody.Name == nil || *updatedBody.Name != "updated-broker" {
		t.Error("expected name in update request")
	}
	if updatedBody.PersistenceMode == nil || *updatedBody.PersistenceMode != "none" {
		t.Error("expected persistenceMode in update request")
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

	r := newTestMessagingResource(c)

	base := MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("old"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	}
	plan := base
	plan.Name = types.StringValue("new")

	state := buildMessagingInstanceState(t, base)
	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: buildMessagingInstancePlan(t, plan), State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on update")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiMessagingInstance{ID: "mq-123", Status: "deleting"})
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

	r := newTestMessagingResource(c)

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("my-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

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

	r := newTestMessagingResource(c)

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-gone"),
		Name:            types.StringValue("gone"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

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
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/messaging/mq-123":
			// Return a server error during the delete-poll loop.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &messagingInstanceResource{client: c, pollInterval: 5 * time.Millisecond, pollTimeout: 60 * time.Millisecond}

	state := buildMessagingInstanceState(t, MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("x"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

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
	// Exercise the default getPollInterval/getPollTimeout branches via a
	// zero-value resource (Read does not poll, so the defaults are only
	// constructed, not waited on).
	r := &messagingInstanceResource{}
	if got := r.getPollInterval(); got != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", got)
	}
	if got := r.getPollTimeout(); got != 30*time.Minute {
		t.Errorf("expected default poll timeout 30m (the platform's CategoryLongRunning saga envelope; readiness alone is budgeted 20m), got %v", got)
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	importResp := resource.ImportStateResponse{State: emptyMessagingInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "mq-123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	var result MessagingInstanceModel
	importResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "mq-123" {
		t.Errorf("expected imported ID mq-123, got %s", result.ID.ValueString())
	}
}

// TestUpdateFlavorChangeRejected pins the apply-time refusal of a flavor change.
// The plan-time modifier only WARNS (an error there would block `terraform
// destroy`), so this guard is the only refusal: flavorId is not in the PUT, so
// without it the change is silently discarded and the apply ends in
// "inconsistent result after apply" with no hint that flavor resize is unsupported.
func TestUpdateFlavorChangeRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request on a flavor change: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestMessagingResource(c)

	stateModel := MessagingInstanceModel{
		ID:              types.StringValue("mq-123"),
		Name:            types.StringValue("my-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	}
	state := buildMessagingInstanceState(t, stateModel)

	planModel := stateModel
	planModel.FlavorID = types.StringValue("mq.gp1.large")
	plan := buildMessagingInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error rejecting a flavor_id change")
	}
}

// --- create-timeout orphan contract (adopt-as-tracked) ---

// TestCreateWaitTimeoutAdopts: the create 202's operation never completes inside
// the provider's wait, but the instance exists — the sweep adopts exactly the
// instance this apply produced (name + created-after-apply-start + type), and
// the post-adoption read-persist writes a tracked state row.
func TestCreateWaitTimeoutAdopts(t *testing.T) {
	listed := false
	now := time.Now().UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/v1/tenants/t-1/messaging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1"})
		case p == "/v1/tenants/t-1/operations/op-1" && !listed:
			// Running forever: the wait gives up, the classification reads the
			// operation again (still not terminal → UNKNOWN arm → the sweep).
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "running"})
		case p == "/v1/tenants/t-1/messaging":
			listed = true
			_ = json.NewEncoder(w).Encode(apiMessagingInstanceList{Instances: []apiInstanceRef{
				// A same-named KAFKA sibling must never be adopted — the plan's
				// type keeps it out of the sweep even though everything else
				// (name, freshness) matches.
				{ID: "kafka-fresh", Name: "test-broker", Type: "kafka", CreatedAt: now},
				{ID: "mq-fresh", Name: "test-broker", Type: "lavinmq", CreatedAt: now},
				// An older same-name same-type instance predates this apply.
				{ID: "mq-stale", Name: "test-broker", Type: "lavinmq", CreatedAt: "2020-01-01T00:00:00Z"},
			}})
		case p == "/v1/tenants/t-1/messaging/mq-fresh":
			// The adoption's honest read: the platform's row, running.
			_ = json.NewEncoder(w).Encode(apiMessagingInstance{
				ID: "mq-fresh", Name: "test-broker", Type: "lavinmq", TypeVersion: "2.3",
				FlavorID: "mq.gp1.small", VPCID: "vpc-1", SubnetID: "sn-1",
				PersistenceMode: "persistent", Status: "running",
				PrivateIP: "10.0.1.5", Port: 5672, AMQPSPort: 5671, ManagementPort: 15672,
				CreatedAt: now,
			})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &messagingInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 300 * time.Millisecond}

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("test-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})
	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("an adopted create must carry the adoption warning, not an error: %v", createResp.Diagnostics.Errors())
	}
	if len(createResp.Diagnostics.Warnings()) == 0 {
		t.Fatal("the adoption must be visible as a warning")
	}

	var result MessagingInstanceModel
	if diags := createResp.State.Get(context.Background(), &result); diags.HasError() {
		t.Fatalf("read back adopted state: %v", diags.Errors())
	}
	if result.ID.ValueString() != "mq-fresh" {
		t.Errorf("adopted id = %q, want mq-fresh (the apply's own instance)", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("adopted status = %q, want running (the honest read)", result.Status.ValueString())
	}
}

// TestCreateOperationRefused: a terminal-FAILED operation is the platform's own
// no — nothing was created, the refused arm says so, and re-applying is safe.
func TestCreateOperationRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/v1/tenants/t-1/messaging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-2"})
		case p == "/v1/tenants/t-1/operations/op-2":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-2", Status: "failed", Error: "flavor not available in zone"})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &messagingInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 300 * time.Millisecond}

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("test-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})
	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("a refused create must error, naming the platform's reason")
	}
	found := false
	for _, e := range createResp.Diagnostics.Errors() {
		if strings.Contains(e.Summary(), "Refused By The Platform") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the refused-arm diagnostic, got: %v", createResp.Diagnostics.Errors())
	}
	if !createResp.State.Raw.IsNull() {
		t.Error("a refused create must not record state")
	}
}

// TestCreateWaitTimeoutVerifiedAbsent: the sweep found nothing matching this
// apply — the verified-absence arm: an error, no state, re-apply is safe.
func TestCreateWaitTimeoutVerifiedAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/v1/tenants/t-1/messaging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-3"})
		case p == "/v1/tenants/t-1/operations/op-3":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-3", Status: "running"})
		case p == "/v1/tenants/t-1/messaging":
			_ = json.NewEncoder(w).Encode(apiMessagingInstanceList{})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &messagingInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 300 * time.Millisecond}

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("test-broker"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})
	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("verified absence must error")
	}
	found := false
	for _, e := range createResp.Diagnostics.Errors() {
		if strings.Contains(e.Summary(), "Verified Absent") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the verified-absent diagnostic, got: %v", createResp.Diagnostics.Errors())
	}
	if !createResp.State.Raw.IsNull() {
		t.Error("verified absence must not record state")
	}
}
