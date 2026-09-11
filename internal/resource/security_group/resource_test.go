package security_group

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestSecurityGroupModelFromAPI(t *testing.T) {
	sg := &apiSecurityGroup{
		ID:          "sg-123",
		Name:        "test-sg",
		Description: "A test security group",
		VPCID:       "vpc-456",
		IsDefault:   false,
		Tags:        map[string]string{"env": "test"},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	var model SecurityGroupModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), sg, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ID.ValueString() != "sg-123" {
		t.Errorf("expected ID sg-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-sg" {
		t.Errorf("expected Name test-sg, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "A test security group" {
		t.Errorf("expected Description 'A test security group', got %s", model.Description.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-456" {
		t.Errorf("expected VPCID vpc-456, got %s", model.VPCID.ValueString())
	}
	if model.IsDefault.ValueBool() != false {
		t.Error("expected IsDefault false, got true")
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected CreatedAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestSecurityGroupModelFromAPIMinimal(t *testing.T) {
	sg := &apiSecurityGroup{
		ID:        "sg-789",
		Name:      "minimal-sg",
		IsDefault: true,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model SecurityGroupModel
	model.Description = types.StringNull()
	model.Tags = types.MapNull(types.StringType)

	var diags diag.Diagnostics
	model.fromAPI(context.Background(), sg, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !model.Description.IsNull() {
		t.Errorf("expected Description to be null, got %s", model.Description.ValueString())
	}
	if !model.VPCID.IsNull() {
		t.Errorf("expected VPCID to be null, got %s", model.VPCID.ValueString())
	}
	if !model.Tags.IsNull() {
		t.Error("expected Tags to be null")
	}
	if !model.IsDefault.ValueBool() {
		t.Error("expected IsDefault true, got false")
	}
}

func TestSecurityGroupModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := SecurityGroupModel{
		Name:        types.StringValue("my-sg"),
		Description: types.StringValue("My security group"),
		VPCID:       types.StringValue("vpc-123"),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toCreateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Name != "my-sg" {
		t.Errorf("expected Name my-sg, got %s", req.Name)
	}
	if req.Description != "My security group" {
		t.Errorf("expected Description 'My security group', got %s", req.Description)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected VPCID vpc-123, got %s", req.VPCID)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestSecurityGroupModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "staging"})

	model := SecurityGroupModel{
		Name:        types.StringValue("updated-sg"),
		Description: types.StringValue("Updated"),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toUpdateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if *req.Name != "updated-sg" {
		t.Errorf("expected Name updated-sg, got %s", *req.Name)
	}
	if *req.Description != "Updated" {
		t.Errorf("expected Description 'Updated', got %s", *req.Description)
	}
	if req.Tags["env"] != "staging" {
		t.Errorf("expected tag env=staging, got %v", req.Tags)
	}
}

func TestSecurityGroupResourceCRUD(t *testing.T) {
	sgCreated := apiSecurityGroup{
		ID:        "sg-test-1",
		Name:      "test-sg",
		VPCID:     "vpc-123",
		IsDefault: false,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	sgUpdated := apiSecurityGroup{
		ID:          "sg-test-1",
		Name:        "updated-sg",
		Description: "Updated",
		VPCID:       "vpc-123",
		IsDefault:   false,
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sgCreated)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-test-1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(sgCreated)

		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-test-1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(sgUpdated)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-test-1":
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	ctx := context.Background()

	// Test Create
	createReq := apiCreateSecurityGroupRequest{
		Name:  "test-sg",
		VPCID: "vpc-123",
	}
	apiResp, err := c.Post(ctx, c.TenantPath("/security-groups"), createReq)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var created apiSecurityGroup
	if err := json.Unmarshal(apiResp.Body, &created); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	if created.ID != "sg-test-1" {
		t.Errorf("expected ID sg-test-1, got %s", created.ID)
	}

	// Test Read
	readResp, err := c.Get(ctx, c.TenantPath("/security-groups/sg-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var read apiSecurityGroup
	if err := json.Unmarshal(readResp.Body, &read); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if read.Name != "test-sg" {
		t.Errorf("expected Name test-sg, got %s", read.Name)
	}

	// Test Update
	updateReq := apiUpdateSecurityGroupRequest{}
	name := "updated-sg"
	updateReq.Name = &name
	patchResp, err := c.Put(ctx, c.TenantPath("/security-groups/sg-test-1"), updateReq)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	var updated apiSecurityGroup
	if err := json.Unmarshal(patchResp.Body, &updated); err != nil {
		t.Fatalf("failed to parse update response: %v", err)
	}
	if updated.Name != "updated-sg" {
		t.Errorf("expected Name updated-sg, got %s", updated.Name)
	}

	// Test Delete
	_, err = c.Delete(ctx, c.TenantPath("/security-groups/sg-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestSecurityGroupReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "security group not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	_, err := c.Get(context.Background(), c.TenantPath("/security-groups/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

// --- tfsdk-level resource method tests ---

func sgSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)
	return schemaResp.Schema
}

func sgObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":                    tftypes.String,
			"name":                  tftypes.String,
			"description":           tftypes.String,
			"vpc_id":                tftypes.String,
			"tags":                  tftypes.Map{ElementType: tftypes.String},
			"is_default":            tftypes.Bool,
			"delete_default_egress": tftypes.Bool,
			"created_at":            tftypes.String,
			"timeouts": tftypes.Object{
				AttributeTypes: map[string]tftypes.Type{
					"create": tftypes.String,
					"update": tftypes.String,
					"delete": tftypes.String,
				},
			},
		},
	}
}

func TestNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_security_group" {
		t.Errorf("expected type name frostmoln_security_group, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)

	if resp.Schema.Description == "" {
		t.Error("expected non-empty schema description")
	}
	for _, attr := range []string{"id", "name", "description", "vpc_id", "tags", "is_default", "delete_default_egress", "created_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics)
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: "wrong-type"}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := NewResource()
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestResourceCreate(t *testing.T) {
	sgResp := apiSecurityGroup{
		ID:        "sg-new-1",
		Name:      "web-sg",
		VPCID:     "vpc-abc",
		IsDefault: false,
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sgResp)
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgSchema(t)
	planVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":                  tftypes.NewValue(tftypes.String, "web-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state SecurityGroupModel
	resp.State.Get(context.Background(), &state)

	if state.ID.ValueString() != "sg-new-1" {
		t.Errorf("expected ID sg-new-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "web-sg" {
		t.Errorf("expected Name web-sg, got %s", state.Name.ValueString())
	}
}

func TestResourceRead(t *testing.T) {
	sgResp := apiSecurityGroup{
		ID:          "sg-read-1",
		Name:        "read-sg",
		Description: "test desc",
		IsDefault:   false,
		CreatedAt:   "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-read-1" {
			_ = json.NewEncoder(w).Encode(sgResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgSchema(t)
	stateVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, "sg-read-1"),
		"name":                  tftypes.NewValue(tftypes.String, "read-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model SecurityGroupModel
	resp.State.Get(context.Background(), &model)
	if model.Name.ValueString() != "read-sg" {
		t.Errorf("expected Name read-sg, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "test desc" {
		t.Errorf("expected Description 'test desc', got %s", model.Description.ValueString())
	}
}

func TestResourceReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgSchema(t)
	stateVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, "sg-gone"),
		"name":                  tftypes.NewValue(tftypes.String, "gone-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	// State should be empty (resource removed)
	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be null after not found")
	}
}

func TestResourceUpdate(t *testing.T) {
	// The in-place update MUST be PUT. network registers PUT /security-groups/{id};
	// NOBODY registers PATCH — not network, not provisioning — so a PATCH
	// matches no api-gateway rule and comes back PATH_NOT_ROUTED. The fake
	// answers on ANY verb so a regression reads as "wrong verb", not as a 404.
	var updateMethod string

	sgResp := apiSecurityGroup{
		ID:          "sg-upd-1",
		Name:        "updated-name",
		Description: "updated desc",
		IsDefault:   false,
		CreatedAt:   "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/security-groups/sg-upd-1" {
			updateMethod = r.Method
			_ = json.NewEncoder(w).Encode(sgResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgSchema(t)
	stateVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, "sg-upd-1"),
		"name":                  tftypes.NewValue(tftypes.String, "old-name"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})

	planVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, "sg-upd-1"),
		"name":                  tftypes.NewValue(tftypes.String, "updated-name"),
		"description":           tftypes.NewValue(tftypes.String, "updated desc"),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if updateMethod != http.MethodPut {
		t.Fatalf("in-place security-group update sent %s, want PUT (PATCH is routed nowhere)", updateMethod)
	}

	var model SecurityGroupModel
	resp.State.Get(context.Background(), &model)
	if model.Name.ValueString() != "updated-name" {
		t.Errorf("expected Name updated-name, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "updated desc" {
		t.Errorf("expected Description 'updated desc', got %s", model.Description.ValueString())
	}
}

func TestResourceDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-del-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgSchema(t)
	stateVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, "sg-del-1"),
		"name":                  tftypes.NewValue(tftypes.String, "delete-me"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
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

func TestResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := sgSchema(t)
	stateVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, "sg-already-gone"),
		"name":                  tftypes.NewValue(tftypes.String, "gone"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	// Should not error when already deleted
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors when deleting already-gone resource, got %v", resp.Diagnostics)
	}
}

// fastDeleteResource is the configured resource with the operation wait cut to
// milliseconds, the seam the async-delete tests drive.
func fastDeleteResource(t *testing.T, c *client.Client) *securityGroupResource {
	t.Helper()
	r := NewResource().(*securityGroupResource)
	r.pollInterval = 5 * time.Millisecond
	r.pollTimeout = 100 * time.Millisecond
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})
	return r
}

func sgDeleteState(id string) tftypes.Value {
	return tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, id),
		"name":                  tftypes.NewValue(tftypes.String, "delete-me"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})
}

func runSGDelete(t *testing.T, r resource.Resource, id string) *resource.DeleteResponse {
	t.Helper()
	s := sgSchema(t)
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: sgDeleteState(id)},
	}, resp)
	return resp
}

// TestDeleteWaitsForTheDeleteOperation: a security-group delete routes through
// provisioning and answers 202 with an Operation envelope BEFORE the platform
// has decided anything. The destroy is done when the operation says so, not
// when the 202 lands — the group may still be alive behind that envelope.
func TestDeleteWaitsForTheDeleteOperation(t *testing.T) {
	polled := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-del-op":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del", "status": "pending", "resourceType": "security-group",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-del":
			polled++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del", "status": "completed", "resourceType": "security-group",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastDeleteResource(t, c)

	resp := runSGDelete(t, r, "sg-del-op")

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if polled == 0 {
		t.Error("expected the delete operation endpoint to be polled; a 202 envelope alone is not a destroy")
	}
}

// TestDeleteOperationFailureRefusesAndKeepsState: the workflow decided NO and
// said why. Nothing was changed, the group still exists, and the diagnostic
// must carry the platform's own reason so the practitioner knows what to deal
// with before destroying again.
func TestDeleteOperationFailureRefusesAndKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-del-fail":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-fail", "status": "pending", "resourceType": "security-group",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-del-fail":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-fail", "status": "failed", "resourceType": "security-group",
				"error": "gateway holds routes in this group",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastDeleteResource(t, c)

	resp := runSGDelete(t, r, "sg-del-fail")

	if !resp.Diagnostics.HasError() {
		t.Fatal("a delete whose operation the platform refused must error, not report success")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Refused By The Platform") {
		t.Errorf("expected the refused-deletion summary, got %q", summary)
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "gateway holds routes in this group") {
		t.Errorf("the platform's own reason must survive into the diagnostic, got: %s", detail)
	}
}

// TestDeleteAcceptedButUnwatchableIsClassified: the destroy was accepted but
// its envelope parses to nothing, so the workflow cannot be watched from here.
// That is neither a success nor a verified absence — it must read as an
// unknown outcome, never a silent return.
func TestDeleteAcceptedButUnwatchableIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-del-bad":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("not-json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastDeleteResource(t, c)

	resp := runSGDelete(t, r, "sg-del-bad")

	if !resp.Diagnostics.HasError() {
		t.Fatal("an accepted delete whose operation cannot be watched must error, not silently succeed")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Outcome Is Unknown") {
		t.Errorf("expected the unknown-outcome classification, got %q", summary)
	}
}

// sgCreatePlanValue builds a create-plan for the named group: computed
// attributes unknown, all optional attributes unset.
func sgCreatePlanValue(t *testing.T, name string) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":                  tftypes.NewValue(tftypes.String, name),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, nil),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":              tftypes.NewValue(sgObjectType().AttributeTypes["timeouts"], nil),
	})
}

// runSGCreate runs Create against the named plan value.
func runSGCreate(t *testing.T, r resource.Resource, planVal tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := sgSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: planVal},
	}, resp)
	return resp
}

// TestSecurityGroupCreateAdoptsAfterTimeout pins the Gate 3 discovery-adopt
// fallback: a 202 whose operation never completes resolves, once the sweep
// finds exactly one name match created after the floor, into an adopted state
// row and the shared adoption warning — never an error.
func TestSecurityGroupCreateAdoptsAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "security-group",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-adopt-1":
			// The saga never lands while the provider waits.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "security-group",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"securityGroups": []apiSecurityGroup{{
					ID:        "sg-adopted-1",
					Name:      "adopted-sg",
					CreatedAt: time.Now().UTC().Format(time.RFC3339), // after the floor
				}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-adopted-1":
			_ = json.NewEncoder(w).Encode(apiSecurityGroup{
				ID:        "sg-adopted-1",
				Name:      "adopted-sg",
				IsDefault: false,
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastDeleteResource(t, c)

	resp := runSGCreate(t, r, sgCreatePlanValue(t, "adopted-sg"))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Summary(), "Was Adopted After The Apply Timed Out") {
		t.Fatalf("expected exactly one adoption warning, got %d warning(s)", len(warnings))
	}

	var state SecurityGroupModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "sg-adopted-1" {
		t.Errorf("expected adopted id sg-adopted-1, got %s", state.ID.ValueString())
	}
}

// TestSecurityGroupCreateRefusedByOperation pins the refused arm: the
// operation's terminal failure is the platform deciding NO — an error naming
// the refusal, no adoption sweep (no listing GET), state stays null.
func TestSecurityGroupCreateRefusedByOperation(t *testing.T) {
	listingGets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-refused-1", "status": "pending", "resourceType": "security-group",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-refused-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-refused-1", "status": "failed", "resourceType": "security-group",
				"error": "name already in use",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups":
			listingGets++
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastDeleteResource(t, c)

	resp := runSGCreate(t, r, sgCreatePlanValue(t, "refused-sg"))

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the platform refuses the create")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Refused") {
		t.Errorf("expected a Refused summary, got %s", resp.Diagnostics.Errors()[0].Summary())
	}
	if listingGets != 0 {
		t.Errorf("refused arm must not sweep the listing, got %d listing GET(s)", listingGets)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after a refused create")
	}
}

// Ensure fmt is used.
var _ = fmt.Sprintf
