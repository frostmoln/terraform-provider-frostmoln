package security_group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
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
	return sgStateValWith(id, false)
}

// sgStateValWith builds a populated state value with the given id and
// delete_default_egress value.
func sgStateValWith(id string, deleteDefaultEgress bool) tftypes.Value {
	return tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, id),
		"name":                  tftypes.NewValue(tftypes.String, "web-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, deleteDefaultEgress),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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
		"id":                    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":                  tftypes.NewValue(tftypes.String, "web-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
		"id":                    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":                  tftypes.NewValue(tftypes.String, "web-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
		"id":                    tftypes.NewValue(tftypes.String, "sg-1"),
		"name":                  tftypes.NewValue(tftypes.String, "renamed"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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
		"id":                    tftypes.NewValue(tftypes.String, "sg-1"),
		"name":                  tftypes.NewValue(tftypes.String, "renamed"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, false),
		"is_default":            tftypes.NewValue(tftypes.Bool, false),
		"created_at":            tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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

// --- delete_default_egress (create-time removal of the injected egress pair) ---

// sgCreatePlanVal builds a create plan with the given delete_default_egress
// value (tftypes.Bool-typed: true, false, or nil for null).
func sgCreatePlanVal(deleteDefaultEgress any) tftypes.Value {
	return tftypes.NewValue(sgObjectType(), map[string]tftypes.Value{
		"id":                    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":                  tftypes.NewValue(tftypes.String, "web-sg"),
		"description":           tftypes.NewValue(tftypes.String, nil),
		"vpc_id":                tftypes.NewValue(tftypes.String, "vpc-abc"),
		"tags":                  tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"is_default":            tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"delete_default_egress": tftypes.NewValue(tftypes.Bool, deleteDefaultEgress),
		"created_at":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
}

// defaultEgressRulesBody is the group read-back carrying the two injected
// defaults plus three rules that must survive: an ingress rule with an empty
// remote (wrong direction), an egress rule with a CIDR, and an egress rule
// with a remote group (both wrong remote).
const defaultEgressRulesBody = `{"id":"sg-1","name":"web-sg","rules":[
	{"id":"rule-v4","direction":"egress"},
	{"id":"rule-v6","direction":"egress"},
	{"id":"rule-in","direction":"ingress"},
	{"id":"rule-eg-cidr","direction":"egress","remoteCidr":"0.0.0.0/0"},
	{"id":"rule-eg-group","direction":"egress","remoteSecurityGroupId":"sg-other"}
]}`

// requestLog records handler hits. Handlers run on per-connection goroutines
// and consecutive requests may overlap server-side, so every access is
// mutex-guarded (CI runs -race).
type requestLog struct {
	mu      sync.Mutex
	paths   []string
	deleted map[string]bool
}

func (l *requestLog) record(method, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, method+" "+path)
}

func (l *requestLog) recordDelete(ruleID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deleted[ruleID] = true
}

func (l *requestLog) deletedRules() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]bool, len(l.deleted))
	for k, v := range l.deleted {
		out[k] = v
	}
	return out
}

func (l *requestLog) requestCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.paths)
}

func (l *requestLog) sequence() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

func TestCreateDeletesDefaultEgressRulesWhenOptedIn(t *testing.T) {
	log := &requestLog{deleted: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.Method, r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"sg-1","name":"web-sg","vpcId":"vpc-abc","isDefault":false,"createdAt":"2025-06-01T12:00:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-1":
			_, _ = w.Write([]byte(defaultEgressRulesBody))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/tenants/t-123/security-groups/sg-1/rules/"):
			log.recordDelete(strings.TrimPrefix(r.URL.Path, "/v1/tenants/t-123/security-groups/sg-1/rules/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"not found"}`))
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(true)}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected clean create, got %v", resp.Diagnostics.Errors())
	}
	if len(resp.Diagnostics.Warnings()) != 0 {
		t.Fatalf("expected no warnings, got %v", resp.Diagnostics.Warnings())
	}

	// Both injected defaults — IPv4 and IPv6, matched on direction+empty-prefix,
	// never on family — and nothing else.
	want := map[string]bool{"rule-v4": true, "rule-v6": true}
	if got := log.deletedRules(); !reflect.DeepEqual(got, want) {
		t.Errorf("expected deletes of exactly the two injected defaults %v, got %v", want, got)
	}

	var state SecurityGroupModel
	resp.State.Get(context.Background(), &state)
	if !state.DeleteDefaultEgress.ValueBool() {
		t.Error("expected state to carry delete_default_egress = true")
	}
	if state.ID.ValueString() != "sg-1" {
		t.Errorf("expected state id sg-1, got %s", state.ID.ValueString())
	}
}

func TestCreateLeavesDefaultEgressRulesUnlessOptedIn(t *testing.T) {
	for name, value := range map[string]any{"false": false, "null": nil} {
		t.Run(name, func(t *testing.T) {
			log := &requestLog{deleted: map[string]bool{}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.record(r.Method, r.URL.Path)
				if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups" {
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"sg-1","name":"web-sg","isDefault":false,"createdAt":"2025-06-01T12:00:00Z"}`))
					return
				}
				t.Errorf("no rule listing or deletion may happen without the opt-in, got %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
			c.SetTenantIDForTest("t-123")
			r := configuredSGResource(t, c)

			s := sgSchema(t)
			resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(value)}}, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("expected clean create, got %v", resp.Diagnostics.Errors())
			}
			if log.requestCount() != 1 {
				t.Errorf("expected exactly the create POST, got %d requests", log.requestCount())
			}

			var state SecurityGroupModel
			resp.State.Get(context.Background(), &state)
			if state.DeleteDefaultEgress.ValueBool() {
				t.Error("expected state to carry delete_default_egress = false")
			}
		})
	}
}

func TestCreateDefaultEgressFailureWarnsButCreateSucceeds(t *testing.T) {
	t.Run("listing the rules fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"sg-1","name":"web-sg","isDefault":false,"createdAt":"2025-06-01T12:00:00Z"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-1":
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
		c.SetTenantIDForTest("t-123")
		r := configuredSGResource(t, c)

		s := sgSchema(t)
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(true)}}, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("a failed rule listing must not fail the create — the group is live, got %v", resp.Diagnostics.Errors())
		}
		if !hasWarningSummary(resp.Diagnostics, "Could Not List Default Egress Rules") {
			t.Errorf("expected a %q warning, got %v", "Could Not List Default Egress Rules", resp.Diagnostics.Warnings())
		}
		var state SecurityGroupModel
		resp.State.Get(context.Background(), &state)
		if state.ID.ValueString() != "sg-1" {
			t.Errorf("expected the created group in state, got id %s", state.ID.ValueString())
		}
	})

	t.Run("deleting a default fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"sg-1","name":"web-sg","isDefault":false,"createdAt":"2025-06-01T12:00:00Z"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-1":
				_, _ = w.Write([]byte(defaultEgressRulesBody))
			case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/rules/rule-v4"):
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
			case r.Method == http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
		c.SetTenantIDForTest("t-123")
		r := configuredSGResource(t, c)

		s := sgSchema(t)
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(true)}}, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("a failed default-rule delete must not fail the create — the group is live, got %v", resp.Diagnostics.Errors())
		}
		if !hasWarningSummary(resp.Diagnostics, "Could Not Delete A Default Egress Rule") {
			t.Errorf("expected a %q warning, got %v", "Could Not Delete A Default Egress Rule", resp.Diagnostics.Warnings())
		}
		var state SecurityGroupModel
		resp.State.Get(context.Background(), &state)
		if state.ID.ValueString() != "sg-1" {
			t.Errorf("expected the created group in state, got id %s", state.ID.ValueString())
		}
	})
}

// A group imported (or created before the attribute existed) carries no
// delete_default_egress value. Read adopts the false default, and with state
// false matching the schema default false the next plan is empty by
// construction.
func TestImportOfGroupWithoutAttributePlansEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-old-1" {
			_, _ = w.Write([]byte(`{"id":"sg-old-1","name":"legacy-sg","isDefault":false,"createdAt":"2024-01-01T00:00:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"not found"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	importResp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(sgObjectType(), nil)}}
	r.(resource.ResourceWithImportState).ImportState(context.Background(), resource.ImportStateRequest{ID: "sg-old-1"}, importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: s}}
	r.Read(context.Background(), resource.ReadRequest{State: importResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read after import failed: %v", readResp.Diagnostics.Errors())
	}

	var state SecurityGroupModel
	readResp.State.Get(context.Background(), &state)
	if state.DeleteDefaultEgress.IsNull() || state.DeleteDefaultEgress.ValueBool() {
		t.Errorf("expected delete_default_egress seeded to false after import, got %v", state.DeleteDefaultEgress)
	}
}

func hasWarningSummary(diags diag.Diagnostics, summary string) bool {
	for _, d := range diags.Warnings() {
		if d.Summary() == summary {
			return true
		}
	}
	return false
}

// TestCreateDeletesDefaultEgressRulesWhenOptedInAsync drives the production
// path: 202 → operation poll → group read-back. The read-back embeds the
// rules, so the opt-in must delete from it WITHOUT listing the group again.
func TestCreateDeletesDefaultEgressRulesWhenOptedInAsync(t *testing.T) {
	log := &requestLog{deleted: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.Method, r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","status":"accepted","resourceType":"security_group"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-1":
			_, _ = w.Write([]byte(`{"operationId":"op-1","status":"completed","resourceType":"security_group","resourceId":"sg-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-1":
			_, _ = w.Write([]byte(defaultEgressRulesBody))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/tenants/t-123/security-groups/sg-1/rules/"):
			log.recordDelete(strings.TrimPrefix(r.URL.Path, "/v1/tenants/t-123/security-groups/sg-1/rules/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"not found"}`))
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(true)}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected clean create, got %v", resp.Diagnostics.Errors())
	}
	if len(resp.Diagnostics.Warnings()) != 0 {
		t.Fatalf("expected no warnings, got %v", resp.Diagnostics.Warnings())
	}

	want := map[string]bool{"rule-v4": true, "rule-v6": true}
	if got := log.deletedRules(); !reflect.DeepEqual(got, want) {
		t.Errorf("expected deletes of exactly the two injected defaults %v, got %v", want, got)
	}

	// The group is read exactly once — the post-operation read-back, which the
	// opt-in reuses — and strictly after the operation resolves.
	sequence := log.sequence()
	groupReads := 0
	opResolvedAt, firstGroupReadAt, firstDeleteAt := -1, -1, -1
	for i, req := range sequence {
		switch {
		case req == "GET /v1/tenants/t-123/operations/op-1":
			opResolvedAt = i
		case req == "GET /v1/tenants/t-123/security-groups/sg-1":
			groupReads++
			if firstGroupReadAt == -1 {
				firstGroupReadAt = i
			}
		case strings.HasPrefix(req, "DELETE /v1/tenants/t-123/security-groups/sg-1/rules/") && firstDeleteAt == -1:
			firstDeleteAt = i
		}
	}
	if groupReads != 1 {
		t.Errorf("expected exactly one group read (the reused read-back), got %d in %v", groupReads, sequence)
	}
	if opResolvedAt == -1 || firstGroupReadAt == -1 || firstDeleteAt == -1 ||
		opResolvedAt >= firstGroupReadAt || firstGroupReadAt >= firstDeleteAt {
		t.Errorf("expected operation poll → group read-back → deletes, got %v", sequence)
	}
}

// The injected pair is documented as unconditional, so an opt-in create that
// matches nothing must say the platform changed — not report success.
func TestCreateWarnsWhenNoDefaultEgressRulesFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"sg-1","name":"web-sg","isDefault":false,"createdAt":"2025-06-01T12:00:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-1":
			_, _ = w.Write([]byte(`{"id":"sg-1","name":"web-sg","rules":[
				{"id":"rule-eg-cidr","direction":"egress","remoteCidr":"0.0.0.0/0"}
			]}`))
		case r.Method == http.MethodDelete:
			t.Errorf("nothing matches the injected pair, so nothing may be deleted, got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := configuredSGResource(t, c)

	s := sgSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(true)}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing injected pair must not fail the create, got %v", resp.Diagnostics.Errors())
	}
	if !hasWarningSummary(resp.Diagnostics, "No Default Egress Rules Found") {
		t.Errorf("expected a %q warning, got %v", "No Default Egress Rules Found", resp.Diagnostics.Warnings())
	}
}

// Flipping delete_default_egress on a group that already exists is a
// documented no-op — the plan has to say so, because a schema description
// cannot reach a CI apply.
func TestModifyPlanWarnsWhenFlippingDeleteDefaultEgress(t *testing.T) {
	r := NewResource()
	s := sgSchema(t)

	for name, tc := range map[string]struct {
		stateValue, planValue bool
	}{
		"false to true": {stateValue: false, planValue: true},
		"true to false": {stateValue: true, planValue: false},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ModifyPlanResponse{}
			r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
				Plan:  tfsdk.Plan{Schema: s, Raw: sgStateValWith("sg-1", tc.planValue)},
				State: tfsdk.State{Schema: s, Raw: sgStateValWith("sg-1", tc.stateValue)},
			}, resp)
			if !hasWarningSummary(resp.Diagnostics, "delete_default_egress Is Create-Time Only") {
				t.Errorf("expected a %q warning, got %v", "delete_default_egress Is Create-Time Only", resp.Diagnostics.Warnings())
			}
			if resp.Diagnostics.HasError() {
				t.Errorf("the warning must not fail the plan, got %v", resp.Diagnostics.Errors())
			}
		})
	}
}

func TestModifyPlanSilentWhenValueUnchangedOrCreating(t *testing.T) {
	r := NewResource()
	s := sgSchema(t)

	t.Run("unchanged", func(t *testing.T) {
		resp := &resource.ModifyPlanResponse{}
		r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Plan:  tfsdk.Plan{Schema: s, Raw: sgStateValWith("sg-1", true)},
			State: tfsdk.State{Schema: s, Raw: sgStateValWith("sg-1", true)},
		}, resp)
		if len(resp.Diagnostics.Warnings()) != 0 {
			t.Errorf("an unchanged value must not warn, got %v", resp.Diagnostics.Warnings())
		}
	})

	t.Run("create", func(t *testing.T) {
		resp := &resource.ModifyPlanResponse{}
		r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Plan:  tfsdk.Plan{Schema: s, Raw: sgCreatePlanVal(true)},
			State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(sgObjectType(), nil)},
		}, resp)
		if len(resp.Diagnostics.Warnings()) != 0 {
			t.Errorf("a create must not warn, got %v", resp.Diagnostics.Warnings())
		}
	})
}
