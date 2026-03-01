package security_group_rule

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
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
