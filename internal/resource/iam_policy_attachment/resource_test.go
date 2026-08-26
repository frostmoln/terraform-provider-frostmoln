package iam_policy_attachment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func meServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-1", "tenantId": "tenant-1", "email": "t@example.com"})
			return
		}
		handler(w, r)
	}))
}

func configured(t *testing.T, url string) *iamPolicyAttachmentResource {
	t.Helper()
	c := client.NewClient(url, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return &iamPolicyAttachmentResource{client: c}
}

func attSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &iamPolicyAttachmentResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func attState(tfType tftypes.Type, id, policyID, typ, attacheeID string) tftypes.Value {
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, id),
		"policy_id":     tftypes.NewValue(tftypes.String, policyID),
		"attachee_type": tftypes.NewValue(tftypes.String, typ),
		"attachee_id":   tftypes.NewValue(tftypes.String, attacheeID),
		"attached_at":   tftypes.NewValue(tftypes.String, "2026-07-08T00:00:00Z"),
	})
}

func TestComposeID(t *testing.T) {
	if got := composeID("pol-1", "api_key", "ak-1"); got != "pol-1/api_key/ak-1" {
		t.Errorf("composeID = %q", got)
	}
}

func TestFindAttachment_Paginates(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/iam/policies/pol-1/attachments" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// First page (no cursor) returns a non-matching row + a next cursor;
		// the target only appears on page 2.
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(apiAttachmentList{
				Attachments: []apiAttachment{{PolicyID: "pol-1", AttacheeType: "group", AttacheeID: "g-1", AttachedAt: "t0"}},
				NextCursor:  "c2",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(apiAttachmentList{
			Attachments: []apiAttachment{{PolicyID: "pol-1", AttacheeType: "api_key", AttacheeID: "ak-1", AttachedAt: "t1"}},
			NextCursor:  "",
		})
	})
	defer server.Close()

	r := configured(t, server.URL)
	att, found, err := r.findAttachment(context.Background(), "pol-1", "api_key", "ak-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !found || att.AttachedAt != "t1" {
		t.Errorf("expected to find the attachment on page 2, got found=%v att=%+v", found, att)
	}
}

func TestFindAttachment_FilterFastPath(t *testing.T) {
	var listCalls int
	var gotType, gotID string
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/iam/policies/pol-1/attachments" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		listCalls++
		gotType = r.URL.Query().Get("attacheeType")
		gotID = r.URL.Query().Get("attacheeId")
		// A filter-aware identity answers a single-attachment lookup with just the
		// one match and an empty cursor — the loop must then stop after one request.
		_ = json.NewEncoder(w).Encode(apiAttachmentList{
			Attachments: []apiAttachment{{PolicyID: "pol-1", AttacheeType: "api_key", AttacheeID: "ak-1", AttachedAt: "t1"}},
			NextCursor:  "",
		})
	})
	defer server.Close()

	r := configured(t, server.URL)
	att, found, err := r.findAttachment(context.Background(), "pol-1", "api_key", "ak-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !found || att.AttachedAt != "t1" {
		t.Errorf("expected the filtered match, got found=%v att=%+v", found, att)
	}
	if listCalls != 1 {
		t.Errorf("the filter fast path must issue exactly ONE list request, got %d", listCalls)
	}
	if gotType != "api_key" || gotID != "ak-1" {
		t.Errorf("the attacheeType/attacheeId filter was not sent: type=%q id=%q", gotType, gotID)
	}
}

func TestCreate_AttachesAndReadsBack(t *testing.T) {
	attached := false
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/iam/policies/pol-1/attachments":
			var req apiAttachRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.AttacheeType != "api_key" || req.AttacheeID != "ak-1" {
				t.Errorf("unexpected attach body: %+v", req)
			}
			attached = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/iam/policies/pol-1/attachments":
			_ = json.NewEncoder(w).Encode(apiAttachmentList{
				Attachments: []apiAttachment{{PolicyID: "pol-1", AttacheeType: "api_key", AttacheeID: "ak-1", AttachedAt: "t1"}},
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configured(t, server.URL)
	s := attSchema(t)
	tfType := s.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"policy_id":     tftypes.NewValue(tftypes.String, "pol-1"),
		"attachee_type": tftypes.NewValue(tftypes.String, "api_key"),
		"attachee_id":   tftypes.NewValue(tftypes.String, "ak-1"),
		"attached_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	res.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	if !attached {
		t.Error("expected attach POST")
	}
	var m IAMPolicyAttachmentModel
	resp.State.Get(ctx, &m)
	if m.ID.ValueString() != "pol-1/api_key/ak-1" {
		t.Errorf("id = %s", m.ID.ValueString())
	}
	if m.AttachedAt.ValueString() != "t1" {
		t.Errorf("attached_at = %s", m.AttachedAt.ValueString())
	}
}

func TestRead_GoneRemovesResource(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The list no longer contains our attachee -> drift removal.
		_ = json.NewEncoder(w).Encode(apiAttachmentList{Attachments: []apiAttachment{}})
	})
	defer server.Close()

	ctx := context.Background()
	res := configured(t, server.URL)
	s := attSchema(t)
	tfType := s.Type().TerraformType(ctx)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s}}
	res.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: attState(tfType, "pol-1/api_key/ak-1", "pol-1", "api_key", "ak-1")}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after the attachment is gone")
	}
}

func TestDelete_UsesQueryParams(t *testing.T) {
	var gotType, gotID string
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-1/iam/policies/pol-1/attachments" {
			gotType = r.URL.Query().Get("attacheeType")
			gotID = r.URL.Query().Get("attacheeId")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configured(t, server.URL)
	s := attSchema(t)
	tfType := s.Type().TerraformType(ctx)

	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: attState(tfType, "pol-1/api_key/ak-1", "pol-1", "api_key", "ak-1")}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	if gotType != "api_key" || gotID != "ak-1" {
		t.Errorf("detach query = type %q id %q", gotType, gotID)
	}
}

func TestImportState(t *testing.T) {
	r := &iamPolicyAttachmentResource{}
	s := attSchema(t)
	tfType := s.Type().TerraformType(context.Background())
	initVal := attState(tfType, "", "", "", "")

	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: initVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "pol-1/api_key/ak-1"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var m IAMPolicyAttachmentModel
	resp.State.Get(context.Background(), &m)
	if m.PolicyID.ValueString() != "pol-1" || m.AttacheeType.ValueString() != "api_key" || m.AttacheeID.ValueString() != "ak-1" {
		t.Errorf("parsed import wrong: %+v", m)
	}
}

func TestImportState_Invalid(t *testing.T) {
	r := &iamPolicyAttachmentResource{}
	s := attSchema(t)
	tfType := s.Type().TerraformType(context.Background())
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: attState(tfType, "", "", "", "")}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "not-composite"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for a malformed import id")
	}
}
