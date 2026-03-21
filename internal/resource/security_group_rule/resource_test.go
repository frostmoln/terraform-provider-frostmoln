package security_group_rule

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestSecurityGroupRuleModelFromAPI(t *testing.T) {
	portMin := 80
	portMax := 443
	rule := &apiSecurityGroupRule{
		ID:           "rule-123",
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: &portMin,
		PortRangeMax: &portMax,
		RemoteCIDR:   "0.0.0.0/0",
		Description:  "HTTP and HTTPS",
	}

	var model SecurityGroupRuleModel
	model.fromAPI("sg-456", rule)

	if model.ID.ValueString() != "rule-123" {
		t.Errorf("expected ID rule-123, got %s", model.ID.ValueString())
	}
	if model.SecurityGroupID.ValueString() != "sg-456" {
		t.Errorf("expected SecurityGroupID sg-456, got %s", model.SecurityGroupID.ValueString())
	}
	if model.Direction.ValueString() != "ingress" {
		t.Errorf("expected Direction ingress, got %s", model.Direction.ValueString())
	}
	if model.Protocol.ValueString() != "tcp" {
		t.Errorf("expected Protocol tcp, got %s", model.Protocol.ValueString())
	}
	if model.PortRangeMin.ValueInt64() != 80 {
		t.Errorf("expected PortRangeMin 80, got %d", model.PortRangeMin.ValueInt64())
	}
	if model.PortRangeMax.ValueInt64() != 443 {
		t.Errorf("expected PortRangeMax 443, got %d", model.PortRangeMax.ValueInt64())
	}
	if model.RemoteCIDR.ValueString() != "0.0.0.0/0" {
		t.Errorf("expected RemoteCIDR 0.0.0.0/0, got %s", model.RemoteCIDR.ValueString())
	}
	if model.Description.ValueString() != "HTTP and HTTPS" {
		t.Errorf("expected Description 'HTTP and HTTPS', got %s", model.Description.ValueString())
	}
	if !model.RemoteGroupID.IsNull() {
		t.Error("expected RemoteGroupID to be null")
	}
}

func TestSecurityGroupRuleModelFromAPIMinimal(t *testing.T) {
	rule := &apiSecurityGroupRule{
		ID:        "rule-789",
		Direction: "egress",
		Protocol:  "any",
	}

	var model SecurityGroupRuleModel
	model.fromAPI("sg-456", rule)

	if model.ID.ValueString() != "rule-789" {
		t.Errorf("expected ID rule-789, got %s", model.ID.ValueString())
	}
	if !model.PortRangeMin.IsNull() {
		t.Error("expected PortRangeMin to be null")
	}
	if !model.PortRangeMax.IsNull() {
		t.Error("expected PortRangeMax to be null")
	}
	if !model.RemoteCIDR.IsNull() {
		t.Error("expected RemoteCIDR to be null")
	}
	if !model.RemoteGroupID.IsNull() {
		t.Error("expected RemoteGroupID to be null")
	}
	if !model.Description.IsNull() {
		t.Error("expected Description to be null")
	}
}

func TestSecurityGroupRuleModelToCreateRequest(t *testing.T) {
	model := SecurityGroupRuleModel{
		SecurityGroupID: types.StringValue("sg-123"),
		Direction:       types.StringValue("ingress"),
		Protocol:        types.StringValue("tcp"),
		PortRangeMin:    types.Int64Value(22),
		PortRangeMax:    types.Int64Value(22),
		RemoteCIDR:      types.StringValue("10.0.0.0/8"),
		RemoteGroupID:   types.StringNull(),
		Description:     types.StringValue("SSH"),
	}

	req := model.toCreateRequest()

	if req.Direction != "ingress" {
		t.Errorf("expected Direction ingress, got %s", req.Direction)
	}
	if req.Protocol != "tcp" {
		t.Errorf("expected Protocol tcp, got %s", req.Protocol)
	}
	if req.PortRangeMin == nil || *req.PortRangeMin != 22 {
		t.Errorf("expected PortRangeMin 22, got %v", req.PortRangeMin)
	}
	if req.PortRangeMax == nil || *req.PortRangeMax != 22 {
		t.Errorf("expected PortRangeMax 22, got %v", req.PortRangeMax)
	}
	if req.RemoteCIDR != "10.0.0.0/8" {
		t.Errorf("expected RemoteCIDR 10.0.0.0/8, got %s", req.RemoteCIDR)
	}
	if req.RemoteGroupID != "" {
		t.Errorf("expected RemoteGroupID empty, got %s", req.RemoteGroupID)
	}
	if req.Description != "SSH" {
		t.Errorf("expected Description SSH, got %s", req.Description)
	}
}

func TestSecurityGroupRuleResourceCRUD(t *testing.T) {
	portMin := 443
	portMax := 443

	ruleCreated := apiSecurityGroupRule{
		ID:           "rule-test-1",
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: &portMin,
		PortRangeMax: &portMax,
		RemoteCIDR:   "0.0.0.0/0",
		Description:  "HTTPS",
	}

	sgWithRules := apiSecurityGroupWithRules{
		ID:    "sg-123",
		Rules: []apiSecurityGroupRule{ruleCreated},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123/rules":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(ruleCreated)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(sgWithRules)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123/rules/rule-test-1":
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	ctx := context.Background()

	// Test Create
	createReq := apiCreateSecurityGroupRuleRequest{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: &portMin,
		PortRangeMax: &portMax,
		RemoteCIDR:   "0.0.0.0/0",
		Description:  "HTTPS",
	}
	apiResp, err := c.Post(ctx, c.TenantPath("/security-groups/sg-123/rules"), createReq)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var created apiSecurityGroupRule
	if err := json.Unmarshal(apiResp.Body, &created); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	if created.ID != "rule-test-1" {
		t.Errorf("expected ID rule-test-1, got %s", created.ID)
	}

	// Test Read via parent SG
	readResp, err := c.Get(ctx, c.TenantPath("/security-groups/sg-123"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var sg apiSecurityGroupWithRules
	if err := json.Unmarshal(readResp.Body, &sg); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if len(sg.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(sg.Rules))
	}
	if sg.Rules[0].ID != "rule-test-1" {
		t.Errorf("expected rule ID rule-test-1, got %s", sg.Rules[0].ID)
	}

	// Test Delete
	_, err = c.Delete(ctx, c.TenantPath("/security-groups/sg-123/rules/rule-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestSecurityGroupRuleReadRuleNotFoundInSG(t *testing.T) {
	// SG exists but rule is not in it
	sgWithNoRules := apiSecurityGroupWithRules{
		ID:    "sg-123",
		Rules: []apiSecurityGroupRule{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(sgWithNoRules)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	// The SG itself is found, but the rule won't be in its list
	apiResp, err := c.Get(context.Background(), c.TenantPath("/security-groups/sg-123"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sg apiSecurityGroupWithRules
	if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Verify the rule is not found
	var found *apiSecurityGroupRule
	for i := range sg.Rules {
		if sg.Rules[i].ID == "rule-nonexistent" {
			found = &sg.Rules[i]
			break
		}
	}
	if found != nil {
		t.Error("expected rule to not be found")
	}
}

// --- tfsdk-level resource method tests ---

func sgrSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func sgrObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":                tftypes.String,
			"security_group_id": tftypes.String,
			"direction":         tftypes.String,
			"protocol":          tftypes.String,
			"port_range_min":    tftypes.Number,
			"port_range_max":    tftypes.Number,
			"remote_cidr":       tftypes.String,
			"remote_group_id":   tftypes.String,
			"description":       tftypes.String,
		},
	}
}

func TestRuleNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestRuleMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_security_group_rule" {
		t.Errorf("expected type name frostmoln_security_group_rule, got %s", resp.TypeName)
	}
}

func TestRuleSchema(t *testing.T) {
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)

	for _, attr := range []string{"id", "security_group_id", "direction", "protocol", "port_range_min", "port_range_max", "remote_cidr", "remote_group_id", "description"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestRuleConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestRuleConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: 42}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestRuleConfigureValidClient(t *testing.T) {
	r := NewResource()
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestRuleResourceCreate(t *testing.T) {
	portMin := 443
	portMax := 443
	ruleResp := apiSecurityGroupRule{
		ID:           "rule-new-1",
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: &portMin,
		PortRangeMax: &portMax,
		RemoteCIDR:   "0.0.0.0/0",
		Description:  "HTTPS",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123/rules" {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(ruleResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgrSchema(t)
	planVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"security_group_id": tftypes.NewValue(tftypes.String, "sg-123"),
		"direction":         tftypes.NewValue(tftypes.String, "ingress"),
		"protocol":          tftypes.NewValue(tftypes.String, "tcp"),
		"port_range_min":    tftypes.NewValue(tftypes.Number, 443),
		"port_range_max":    tftypes.NewValue(tftypes.Number, 443),
		"remote_cidr":       tftypes.NewValue(tftypes.String, "0.0.0.0/0"),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, "HTTPS"),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state SecurityGroupRuleModel
	resp.State.Get(context.Background(), &state)

	if state.ID.ValueString() != "rule-new-1" {
		t.Errorf("expected ID rule-new-1, got %s", state.ID.ValueString())
	}
	if state.SecurityGroupID.ValueString() != "sg-123" {
		t.Errorf("expected SecurityGroupID sg-123, got %s", state.SecurityGroupID.ValueString())
	}
	if state.PortRangeMin.ValueInt64() != 443 {
		t.Errorf("expected PortRangeMin 443, got %d", state.PortRangeMin.ValueInt64())
	}
}

func TestRuleResourceRead(t *testing.T) {
	portMin := 80
	portMax := 80
	sgWithRules := apiSecurityGroupWithRules{
		ID: "sg-123",
		Rules: []apiSecurityGroupRule{
			{
				ID:           "rule-read-1",
				Direction:    "ingress",
				Protocol:     "tcp",
				PortRangeMin: &portMin,
				PortRangeMax: &portMax,
				RemoteCIDR:   "10.0.0.0/8",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123" {
			json.NewEncoder(w).Encode(sgWithRules)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgrSchema(t)
	stateVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "rule-read-1"),
		"security_group_id": tftypes.NewValue(tftypes.String, "sg-123"),
		"direction":         tftypes.NewValue(tftypes.String, "ingress"),
		"protocol":          tftypes.NewValue(tftypes.String, "tcp"),
		"port_range_min":    tftypes.NewValue(tftypes.Number, 80),
		"port_range_max":    tftypes.NewValue(tftypes.Number, 80),
		"remote_cidr":       tftypes.NewValue(tftypes.String, "10.0.0.0/8"),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model SecurityGroupRuleModel
	resp.State.Get(context.Background(), &model)
	if model.ID.ValueString() != "rule-read-1" {
		t.Errorf("expected ID rule-read-1, got %s", model.ID.ValueString())
	}
	if model.RemoteCIDR.ValueString() != "10.0.0.0/8" {
		t.Errorf("expected RemoteCIDR 10.0.0.0/8, got %s", model.RemoteCIDR.ValueString())
	}
}

func TestRuleResourceReadRuleNotFoundRemovesState(t *testing.T) {
	// SG exists but rule is not in it
	sgWithRules := apiSecurityGroupWithRules{
		ID:    "sg-123",
		Rules: []apiSecurityGroupRule{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123" {
			json.NewEncoder(w).Encode(sgWithRules)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgrSchema(t)
	stateVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "rule-missing"),
		"security_group_id": tftypes.NewValue(tftypes.String, "sg-123"),
		"direction":         tftypes.NewValue(tftypes.String, "ingress"),
		"protocol":          tftypes.NewValue(tftypes.String, "tcp"),
		"port_range_min":    tftypes.NewValue(tftypes.Number, nil),
		"port_range_max":    tftypes.NewValue(tftypes.Number, nil),
		"remote_cidr":       tftypes.NewValue(tftypes.String, nil),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be null when rule is not found")
	}
}

func TestRuleResourceReadSGNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgrSchema(t)
	stateVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "rule-orphan"),
		"security_group_id": tftypes.NewValue(tftypes.String, "sg-gone"),
		"direction":         tftypes.NewValue(tftypes.String, "ingress"),
		"protocol":          tftypes.NewValue(tftypes.String, "tcp"),
		"port_range_min":    tftypes.NewValue(tftypes.Number, nil),
		"port_range_max":    tftypes.NewValue(tftypes.Number, nil),
		"remote_cidr":       tftypes.NewValue(tftypes.String, nil),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be null when parent SG is not found")
	}
}

func TestRuleResourceUpdateUnsupported(t *testing.T) {
	r := NewResource()
	resp := &resource.UpdateResponse{}
	r.Update(context.Background(), resource.UpdateRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for unsupported update")
	}
}

func TestRuleResourceDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-123/rules/rule-del-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgrSchema(t)
	stateVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "rule-del-1"),
		"security_group_id": tftypes.NewValue(tftypes.String, "sg-123"),
		"direction":         tftypes.NewValue(tftypes.String, "ingress"),
		"protocol":          tftypes.NewValue(tftypes.String, "tcp"),
		"port_range_min":    tftypes.NewValue(tftypes.Number, nil),
		"port_range_max":    tftypes.NewValue(tftypes.Number, nil),
		"remote_cidr":       tftypes.NewValue(tftypes.String, nil),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !deleted {
		t.Error("expected delete to be called")
	}
}

func TestRuleResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgrSchema(t)
	stateVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "rule-gone"),
		"security_group_id": tftypes.NewValue(tftypes.String, "sg-123"),
		"direction":         tftypes.NewValue(tftypes.String, "egress"),
		"protocol":          tftypes.NewValue(tftypes.String, "any"),
		"port_range_min":    tftypes.NewValue(tftypes.Number, nil),
		"port_range_max":    tftypes.NewValue(tftypes.Number, nil),
		"remote_cidr":       tftypes.NewValue(tftypes.String, nil),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors when deleting already-gone rule, got %v", resp.Diagnostics)
	}
}

func TestRuleImportStateCompositeID(t *testing.T) {
	r := NewResource()
	s := sgrSchema(t)

	// Initialize state with null values so SetAttribute works.
	initVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, nil),
		"security_group_id": tftypes.NewValue(tftypes.String, nil),
		"direction":         tftypes.NewValue(tftypes.String, nil),
		"protocol":          tftypes.NewValue(tftypes.String, nil),
		"port_range_min":    tftypes.NewValue(tftypes.Number, nil),
		"port_range_max":    tftypes.NewValue(tftypes.Number, nil),
		"remote_cidr":       tftypes.NewValue(tftypes.String, nil),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: initVal},
	}

	r.(resource.ResourceWithImportState).ImportState(context.Background(), resource.ImportStateRequest{ID: "sg-abc/rule-xyz"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model SecurityGroupRuleModel
	resp.State.Get(context.Background(), &model)

	if model.SecurityGroupID.ValueString() != "sg-abc" {
		t.Errorf("expected SecurityGroupID sg-abc, got %s", model.SecurityGroupID.ValueString())
	}
	if model.ID.ValueString() != "rule-xyz" {
		t.Errorf("expected ID rule-xyz, got %s", model.ID.ValueString())
	}
}

func TestRuleImportStateInvalidID(t *testing.T) {
	r := NewResource()
	s := sgrSchema(t)

	// Initialize state with null values so SetAttribute works.
	initVal := tftypes.NewValue(sgrObjectType(), map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, nil),
		"security_group_id": tftypes.NewValue(tftypes.String, nil),
		"direction":         tftypes.NewValue(tftypes.String, nil),
		"protocol":          tftypes.NewValue(tftypes.String, nil),
		"port_range_min":    tftypes.NewValue(tftypes.Number, nil),
		"port_range_max":    tftypes.NewValue(tftypes.Number, nil),
		"remote_cidr":       tftypes.NewValue(tftypes.String, nil),
		"remote_group_id":   tftypes.NewValue(tftypes.String, nil),
		"description":       tftypes.NewValue(tftypes.String, nil),
	})

	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: initVal},
	}

	r.(resource.ResourceWithImportState).ImportState(context.Background(), resource.ImportStateRequest{ID: "no-slash"}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for invalid import ID format")
	}
}

// Ensure fmt is used.
var _ = fmt.Sprintf
