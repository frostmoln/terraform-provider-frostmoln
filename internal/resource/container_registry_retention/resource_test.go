package container_registry_retention

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

const wantPath = "/v1/tenants/tenant-456/registry/retention"

func newServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
			return
		}
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func configured(t *testing.T, url string) *retentionResource {
	t.Helper()
	c := client.NewClient(url, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &retentionResource{client: c}
}

func retSchema() schema.Schema {
	resp := resource.SchemaResponse{}
	(&retentionResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func num(v any) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }

func value(id any, keep, days any) tftypes.Value {
	return tftypes.NewValue(retSchema().Type().TerraformType(context.Background()), map[string]tftypes.Value{
		"id":                         tftypes.NewValue(tftypes.String, id),
		"keep_last_tagged":           num(keep),
		"delete_untagged_after_days": num(days),
	})
}

func stateModel(t *testing.T, s tfsdk.State) RetentionModel {
	t.Helper()
	var m RetentionModel
	if d := s.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state get: %v", d.Errors())
	}
	return m
}

func writeErr(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"code":"`+code+`","message":"refused","details":{"reason":"`+reason+`"}}`)
}

// Create PUTs only the set rule (an omitted field clears that rule) and
// records the server's answer.
func TestCreatePutsAndRecordsResponse(t *testing.T) {
	var gotMethod string
	var gotBody map[string]any
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"configured":false}`)
			return
		}
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"configured":true,"keepLastTagged":10}`)
	})
	s := retSchema()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}}
	configured(t, srv.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: value(tftypes.UnknownValue, 10, nil)}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	if n := resp.Diagnostics.WarningsCount(); n != 0 {
		t.Errorf("no policy existed, so no replacement warning; got %d", n)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if len(gotBody) != 1 || gotBody["keepLastTagged"] != float64(10) {
		t.Errorf("body = %v, want only keepLastTagged=10", gotBody)
	}
	m := stateModel(t, resp.State)
	if m.ID.ValueString() != "tenant-456" || m.KeepLastTagged.ValueInt64() != 10 || !m.DeleteUntaggedAfterDays.IsNull() {
		t.Errorf("state = %+v", m)
	}
}

func TestUpdateReplacesPolicy(t *testing.T) {
	var gotBody map[string]any
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"configured":true,"deleteUntaggedAfterDays":30}`)
	})
	s := retSchema()
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: value("tenant-456", 10, nil)}}
	configured(t, srv.URL).Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: value("tenant-456", nil, 30)},
		State: tfsdk.State{Schema: s, Raw: value("tenant-456", 10, nil)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if _, ok := gotBody["keepLastTagged"]; ok || gotBody["deleteUntaggedAfterDays"] != float64(30) {
		t.Errorf("body = %v, want only deleteUntaggedAfterDays=30", gotBody)
	}
	m := stateModel(t, resp.State)
	if !m.KeepLastTagged.IsNull() || m.DeleteUntaggedAfterDays.ValueInt64() != 30 {
		t.Errorf("state = %+v", m)
	}
}

func TestCreateRegistryNotEnabled(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusConflict, "invalid_state", reasonNotEnabled)
	})
	s := retSchema()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}}
	configured(t, srv.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: value(tftypes.UnknownValue, 10, nil)}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "not enabled") {
		t.Errorf("summary = %q, want the not-enabled remedy", got)
	}
}

func read(t *testing.T, handler http.HandlerFunc, prior tftypes.Value) *resource.ReadResponse {
	t.Helper()
	srv := newServer(t, handler)
	s := retSchema()
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: prior}}
	configured(t, srv.URL).Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: prior}}, resp)
	return resp
}

func TestReadRecordsPolicy(t *testing.T) {
	resp := read(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"configured":true,"keepLastTagged":5,"deleteUntaggedAfterDays":7}`)
	}, value("tenant-456", 10, nil))
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	m := stateModel(t, resp.State)
	if m.KeepLastTagged.ValueInt64() != 5 || m.DeleteUntaggedAfterDays.ValueInt64() != 7 {
		t.Errorf("state = %+v", m)
	}
}

func TestReadRemovesWhenNotConfigured(t *testing.T) {
	resp := read(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"configured":false}`)
	}, value("tenant-456", 10, nil))
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Errorf("want removal from state, got diags=%v raw=%v", resp.Diagnostics, resp.State.Raw)
	}
}

// A body without `configured` must never be read as "none".
func TestReadMissingConfiguredFailsClosed(t *testing.T) {
	resp := read(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}, value("tenant-456", 10, nil))
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Error("want an error with the resource kept in state")
	}
}

// RETENTION_POLICY_UNRECOGNIZED keeps the resource with both rules null, so the
// plan diffs and the next apply PUTs over it. It must never fail the refresh.
func TestReadUnrecognizedKeepsResourceWithNullRules(t *testing.T) {
	resp := read(t, func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusConflict, "invalid_state", reasonUnrecognized)
	}, value("tenant-456", 10, 30))
	if resp.Diagnostics.HasError() {
		t.Fatalf("refresh must not fail: %v", resp.Diagnostics.Errors())
	}
	m := stateModel(t, resp.State)
	if m.ID.ValueString() != "tenant-456" || !m.KeepLastTagged.IsNull() || !m.DeleteUntaggedAfterDays.IsNull() {
		t.Errorf("state = %+v, want id kept and both rules null", m)
	}
}

func TestReadRegistryNotEnabledRemoves(t *testing.T) {
	resp := read(t, func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusConflict, "invalid_state", reasonNotEnabled)
	}, value("tenant-456", 10, nil))
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Errorf("want removal from state, got diags=%v", resp.Diagnostics)
	}
}

func TestReadRefusesAnotherTenantsID(t *testing.T) {
	resp := read(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no request expected for a foreign tenant id")
	}, value("other-tenant", nil, nil))
	if !resp.Diagnostics.HasError() {
		t.Error("want an error for an import id that is not the provider's tenant")
	}
}

func TestDelete(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"204": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		},
		"registry not enabled": func(w http.ResponseWriter, _ *http.Request) {
			writeErr(w, http.StatusConflict, "invalid_state", reasonNotEnabled)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newServer(t, h)
			resp := &resource.DeleteResponse{}
			configured(t, srv.URL).Delete(context.Background(), resource.DeleteRequest{
				State: tfsdk.State{Schema: retSchema(), Raw: value("tenant-456", 10, nil)},
			}, resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("delete failed: %v", resp.Diagnostics.Errors())
			}
		})
	}
}

func TestValidators(t *testing.T) {
	ctx := context.Background()
	s := retSchema()
	r := &retentionResource{}
	cases := map[string]struct {
		keep, days any
		wantErr    bool
	}{
		"neither set":    {nil, nil, true},
		"keep only":      {1, nil, false},
		"days only":      {nil, 365, false},
		"keep too low":   {0, nil, true},
		"keep too high":  {1001, nil, true},
		"days too low":   {nil, 0, true},
		"days too high":  {nil, 366, true},
		"both in bounds": {1000, 1, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := tfsdk.Config{Schema: s, Raw: value(nil, tc.keep, tc.days)}
			var diags diag.Diagnostics
			for _, v := range r.ConfigValidators(ctx) {
				vr := &resource.ValidateConfigResponse{}
				v.ValidateResource(ctx, resource.ValidateConfigRequest{Config: cfg}, vr)
				diags.Append(vr.Diagnostics...)
			}
			for attr, val := range map[string]any{"keep_last_tagged": tc.keep, "delete_untagged_after_days": tc.days} {
				if val == nil {
					continue
				}
				for _, v := range s.Attributes[attr].(schema.Int64Attribute).Validators {
					vr := &validator.Int64Response{}
					v.ValidateInt64(ctx, validator.Int64Request{
						Path: path.Root(attr), ConfigValue: types.Int64Value(int64(val.(int))),
					}, vr)
					diags.Append(vr.Diagnostics...)
				}
			}
			if diags.HasError() != tc.wantErr {
				t.Errorf("hasError = %v, want %v: %v", diags.HasError(), tc.wantErr, diags)
			}
		})
	}
}

func create(t *testing.T, handler http.HandlerFunc) *resource.CreateResponse {
	t.Helper()
	srv := newServer(t, handler)
	s := retSchema()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}}
	configured(t, srv.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: value(tftypes.UnknownValue, 10, nil)}}, resp)
	return resp
}

// SEC-L2: a policy set outside this configuration is named before it is replaced.
func TestCreateWarnsWhenReplacingAnExistingPolicy(t *testing.T) {
	for name, get := range map[string]func(http.ResponseWriter){
		"configured": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"configured":true,"keepLastTagged":50,"deleteUntaggedAfterDays":3}`)
		},
		"unrecognized": func(w http.ResponseWriter) {
			writeErr(w, http.StatusConflict, "invalid_state", reasonUnrecognized)
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp := create(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					get(w)
					return
				}
				_, _ = io.WriteString(w, `{"configured":true,"keepLastTagged":10}`)
			})
			if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
				t.Fatalf("want exactly one warning, got %v", resp.Diagnostics)
			}
			detail := resp.Diagnostics.Warnings()[0].Detail()
			if name == "configured" && !strings.Contains(detail, "keep_last_tagged = 50") {
				t.Errorf("warning must name the replaced policy: %q", detail)
			}
		})
	}
}

// API-M2: a 403 on PUT names both scopes the route needs.
func TestCreateForbiddenNamesTheDeleteScope(t *testing.T) {
	resp := create(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"configured":false}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"forbidden","message":"forbidden"}`)
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	d := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(d, "registry:artifacts:delete") || !strings.Contains(d, "registry:settings:update") {
		t.Errorf("detail = %q, want both scopes named", d)
	}
}

// SEC-L1 + API-L1: Update and Delete refuse a state id of another tenant before
// any request, and the comparison ignores case.
func TestUpdateAndDeleteRefuseAnotherTenant(t *testing.T) {
	srv := newServer(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("no %s expected for a foreign tenant id", r.Method)
	})
	r := configured(t, srv.URL)
	s := retSchema()
	foreign := value("other-tenant", 10, nil)

	uresp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: foreign}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: foreign}, State: tfsdk.State{Schema: s, Raw: foreign},
	}, uresp)
	if !uresp.Diagnostics.HasError() {
		t.Error("update must refuse another tenant's id")
	}

	dresp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: foreign}}, dresp)
	if !dresp.Diagnostics.HasError() {
		t.Error("delete must refuse another tenant's id")
	}
}

func TestTenantGuardIgnoresCase(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	resp := &resource.DeleteResponse{}
	configured(t, srv.URL).Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: retSchema(), Raw: value("TENANT-456", 10, nil)},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("an upper-cased own tenant id must pass: %v", resp.Diagnostics)
	}
}

// SEC-M1: warn exactly when the plan deletes more than the current state.
func TestModifyPlanWarnsWhenDeletingMore(t *testing.T) {
	s := retSchema()
	null := tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)
	cases := map[string]struct {
		state, plan tftypes.Value
		warn        bool
	}{
		"create":             {null, value(tftypes.UnknownValue, 10, nil), true},
		"destroy":            {value("tenant-456", 10, nil), null, false},
		"rule newly set":     {value("tenant-456", 10, nil), value("tenant-456", 10, 30), true},
		"keep lowered":       {value("tenant-456", 10, nil), value("tenant-456", 5, nil), true},
		"days lowered":       {value("tenant-456", nil, 30), value("tenant-456", nil, 7), true},
		"keep raised":        {value("tenant-456", 10, nil), value("tenant-456", 20, nil), false},
		"rule removed":       {value("tenant-456", 10, 30), value("tenant-456", 10, nil), false},
		"unchanged":          {value("tenant-456", 10, 30), value("tenant-456", 10, 30), false},
		"unknown plan value": {value("tenant-456", 10, nil), value("tenant-456", tftypes.UnknownValue, nil), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: tc.plan}}
			(&retentionResource{}).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
				State: tfsdk.State{Schema: s, Raw: tc.state},
				Plan:  tfsdk.Plan{Schema: s, Raw: tc.plan},
			}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error: %v", resp.Diagnostics)
			}
			if got := resp.Diagnostics.WarningsCount() == 1; got != tc.warn {
				t.Fatalf("warned = %v, want %v", got, tc.warn)
			}
			if tc.warn {
				d := resp.Diagnostics.Warnings()[0].Detail()
				for _, want := range []string{"Before:", "After:", "between 00:00 and 06:00 UTC", "cannot be recovered"} {
					if !strings.Contains(d, want) {
						t.Errorf("detail missing %q: %q", want, d)
					}
				}
			}
		})
	}
}
