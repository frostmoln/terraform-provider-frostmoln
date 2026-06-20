package security_group

import (
	"context"
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

// emptySGState builds a null-valued state of the security group schema.
func emptySGState(t *testing.T) tfsdk.State {
	t.Helper()
	s := sgSchema(t)
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(sgObjectType(), nil)}
}

// sgStateVal builds a populated state value with the given id.
func sgStateVal(id string) tftypes.Value {
	return tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, id),
		"name":        tftypes.NewValue(tftypes.String, "web-sg"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"is_default":  tftypes.NewValue(tftypes.Bool, false),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})
}

func configuredSGResource(t *testing.T, c *client.Client) resource.Resource {
	t.Helper()
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})
	return r
}

// --- Model edge cases ---

func TestModelToUpdateRequestWithTagsAndNullDescription(t *testing.T) {
	ctx := context.Background()
	diags := &diag.Diagnostics{}
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	m := SecurityGroupModel{
		Name:        types.StringValue("sg"),
		Description: types.StringNull(),
		Tags:        tags,
	}
	req := m.toUpdateRequest(ctx, diags)
	if req.Name == nil || *req.Name != "sg" {
		t.Error("expected name set")
	}
	if req.Description == nil || *req.Description != "" {
		t.Error("expected null description to become empty string in update request")
	}
	if req.Tags["env"] != "prod" {
		t.Error("expected tags in update request")
	}
}

func TestModelFromAPIEmptyDescriptionPreservesEmpty(t *testing.T) {
	ctx := context.Background()
	m := SecurityGroupModel{Description: types.StringValue("prior")}
	m.fromAPI(ctx, &apiSecurityGroup{ID: "sg-1", Name: "n", CreatedAt: "t"}, &diag.Diagnostics{})
	if m.Description.IsNull() {
		t.Error("expected non-null empty description when prior was non-null")
	}
}

// --- ImportState ---

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	resp := resource.ImportStateResponse{State: emptySGState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "sg-import-1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "sg-import-1" {
		t.Errorf("expected imported id sg-import-1, got %s", id.ValueString())
	}
}

// --- Error paths ---

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	planVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "web-sg"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"is_default":  tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when create POST fails")
	}
}

func TestCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	planVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "web-sg"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"is_default":  tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when create response body is malformed")
	}
}

func TestReadAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: sgStateVal("sg-1")}}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when read GET returns 500")
	}
}

func TestReadBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: sgStateVal("sg-1")}}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when read response body is malformed")
	}
}

func TestUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	planVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "sg-1"),
		"name":        tftypes.NewValue(tftypes.String, "renamed"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"is_default":  tftypes.NewValue(tftypes.Bool, false),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: sgStateVal("sg-1")},
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when update PATCH returns 500")
	}
}

func TestUpdateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	planVal := tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "sg-1"),
		"name":        tftypes.NewValue(tftypes.String, "renamed"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"is_default":  tftypes.NewValue(tftypes.Bool, false),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: sgStateVal("sg-1")},
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when update response body is malformed")
	}
}

func TestDeleteAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: sgStateVal("sg-1")}}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error when delete returns 500")
	}
}
