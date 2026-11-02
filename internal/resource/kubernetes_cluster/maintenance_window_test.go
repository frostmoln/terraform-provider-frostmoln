package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const refusedWindowMessage = "not permitted: " + maintenanceWindowOperation

// nullUnsetMaintenanceWindow types a model's unset window: the zero types.Object has
// no attribute types, so the framework refuses it.
func nullUnsetMaintenanceWindow(m *KubernetesClusterModel) {
	if len(m.MaintenanceWindow.AttributeTypes(context.Background())) == 0 {
		m.MaintenanceWindow = nullMaintenanceWindow()
	}
}

func (a *tagsAPI) refuseWindowIfSent(w http.ResponseWriter, body map[string]json.RawMessage) bool {
	if _, sent := body["maintenanceWindow"]; !sent || !a.refuseWindow {
		return false
	}
	refusal := a.refusalBody
	if refusal == nil {
		refusal = map[string]string{"code": "insufficient_permission", "message": refusedWindowMessage}
	}
	w.WriteHeader(http.StatusForbidden)
	writeJSON(a.t, w, refusal)
	return true
}

func window(start string, days ...string) *apiMaintenanceWindow {
	return &apiMaintenanceWindow{Days: days, StartTime: start, DurationHours: 6, TimeZone: "Europe/Stockholm"}
}

func runWindowUpdate(t *testing.T, api *tagsAPI, stateM, planM KubernetesClusterModel) resource.UpdateResponse {
	t.Helper()
	r, stop := api.resource(nil)
	defer stop()
	state, plan := buildState(t, stateM), buildPlan(t, planM)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
	return resp
}

// The trap of this field: the plan carries the stored value whether or not the
// configuration names it, and the API refuses its PRESENCE from a Developer or Member.
func TestUpdate_WindowIsSentOnlyWhenItChanges(t *testing.T) {
	stored := window("00:00", "mon", "thu")
	cases := []struct {
		name     string
		plan     *apiMaintenanceWindow
		rename   bool
		tags     bool
		wantPuts int
		wantSent bool
	}{
		{"tags only, server default carried in the plan", stored, false, true, 1, false},
		{"config restates the stored value, with a rename", stored, true, false, 1, false},
		{"same days in another order", window("00:00", "thu", "mon"), true, false, 1, false},
		{"nothing changed", stored, false, false, 0, false},
		{"a real change alone still runs", window("03:00", "mon", "thu"), false, false, 1, true},
		{"a real change of days", window("00:00", "mon"), true, false, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &tagsAPI{t: t, tags: map[string]string{"env": "prod"}, window: stored}
			stateM := stateModel()
			stateM.Tags, stateM.TagsAll = tagMap("env", "prod"), tagMap("env", "prod")
			stateM.MaintenanceWindow = maintenanceWindowFromAPI(stored)
			planM := stateModel()
			planM.Tags, planM.TagsAll = tagMap("env", "prod"), types.MapUnknown(types.StringType)
			planM.MaintenanceWindow = maintenanceWindowFromAPI(tc.plan)
			if tc.rename {
				planM.Name = types.StringValue("renamed")
			}
			if tc.tags {
				planM.Tags = tagMap("env", "dev")
			}
			resp := runWindowUpdate(t, api, stateM, planM)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %v", resp.Diagnostics.Errors())
			}
			if len(api.puts) != tc.wantPuts {
				t.Fatalf("PUTs = %d, want %d: %v", len(api.puts), tc.wantPuts, api.puts)
			}
			if tc.wantPuts == 0 {
				return
			}
			raw, sent := api.puts[0]["maintenanceWindow"]
			if sent != tc.wantSent {
				t.Fatalf("maintenanceWindow sent = %v (%s), want %v", sent, raw, tc.wantSent)
			}
			var got KubernetesClusterModel
			resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
			if !got.MaintenanceWindow.Equal(planM.MaintenanceWindow) {
				t.Errorf("state window = %v, want the plan's %v", got.MaintenanceWindow, planM.MaintenanceWindow)
			}
			if tc.wantSent {
				var body apiMaintenanceWindow
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if body.StartTime != tc.plan.StartTime || body.DurationHours != 6 || body.TimeZone != "Europe/Stockholm" || len(body.Days) != len(tc.plan.Days) {
					t.Errorf("body = %+v, want %+v", body, tc.plan)
				}
			}
		})
	}
}

// retained_volumes_on_destroy alone makes no API call. A window change beside it must
// not be swallowed by that short-circuit.
func TestUpdate_WindowChangeBesideTheDestroyChoiceIsNotSwallowed(t *testing.T) {
	stored := window("00:00", "thu")
	api := &tagsAPI{t: t, window: stored}
	stateM := stateModel()
	stateM.MaintenanceWindow = maintenanceWindowFromAPI(stored)
	planM := stateModel()
	planM.RetainedVolumesOnDestroy = types.StringValue("keep")
	planM.MaintenanceWindow = maintenanceWindowFromAPI(window("03:00", "thu"))
	resp := runWindowUpdate(t, api, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if len(api.puts) != 1 || api.puts[0]["maintenanceWindow"] == nil {
		t.Fatalf("PUTs = %v, want one carrying the window", api.puts)
	}
}

func TestUpdate_WindowRefusalIsReadableAndAppliesNothing(t *testing.T) {
	stored := window("00:00", "thu")
	api := &tagsAPI{t: t, window: stored, refuseWindow: true}
	stateM := stateModel()
	stateM.MaintenanceWindow = maintenanceWindowFromAPI(stored)
	planM := stateModel()
	planM.Name = types.StringValue("renamed")
	planM.MaintenanceWindow = maintenanceWindowFromAPI(window("03:00", "thu"))
	resp := runWindowUpdate(t, api, stateM, planM)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	d := resp.Diagnostics.Errors()[0]
	for _, want := range []string{maintenanceWindowOperation, "nothing in it was applied", "stored value", "Owner and Admin", "access policy"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("detail %q lacks %q", d.Detail(), want)
		}
	}
	var got KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
	if got.Name.ValueString() != "test-cluster" || !got.MaintenanceWindow.Equal(stateM.MaintenanceWindow) {
		t.Errorf("state moved on a refusal: name %s, window %v", got.Name, got.MaintenanceWindow)
	}
}

// The window moved outside Terraform between the refresh and this apply. The planned
// value stands (Terraform refuses a result that differs from a known plan), and nothing
// is sent; the next refresh shows it.
func TestUpdate_WindowMovedSinceTheRefreshKeepsThePlannedValue(t *testing.T) {
	stored := window("00:00", "thu")
	api := &tagsAPI{t: t, window: window("05:00", "fri")}
	stateM := stateModel()
	stateM.MaintenanceWindow = maintenanceWindowFromAPI(stored)
	planM := stateModel()
	planM.Name = types.StringValue("renamed")
	planM.MaintenanceWindow = maintenanceWindowFromAPI(stored)
	resp := runWindowUpdate(t, api, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if _, sent := api.puts[0]["maintenanceWindow"]; sent {
		t.Error("maintenanceWindow sent")
	}
	var got KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
	if !got.MaintenanceWindow.Equal(stateM.MaintenanceWindow) {
		t.Errorf("state window = %v, want the planned value", got.MaintenanceWindow)
	}
}

func windowCreatePlan(w types.Object) KubernetesClusterModel {
	m := pinnedCreatePlan()
	m.AddonVersions = types.MapNull(types.StringType)
	m.MaintenanceWindow = w
	return m
}

func runWindowCreate(t *testing.T, api *tagsAPI, planM KubernetesClusterModel) resource.CreateResponse {
	t.Helper()
	r, stop := api.resource(nil)
	defer stop()
	plan := buildPlan(t, planM)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	return resp
}

func TestCreate_WithoutTheAttributeOmitsTheFieldAndTakesTheServersWindow(t *testing.T) {
	server := window("02:00", "tue")
	api := &tagsAPI{t: t, window: server}
	resp := runWindowCreate(t, api, windowCreatePlan(types.ObjectUnknown(maintenanceWindowAttrTypes)))
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if raw, sent := api.posts[0]["maintenanceWindow"]; sent {
		t.Errorf("maintenanceWindow sent: %s", raw)
	}
	var got KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
	if !got.MaintenanceWindow.Equal(maintenanceWindowFromAPI(server)) {
		t.Errorf("state window = %v, want the server's", got.MaintenanceWindow)
	}
}

func TestCreate_ConfiguredWindowIsSent(t *testing.T) {
	want := window("03:00", "wed")
	api := &tagsAPI{t: t, window: want}
	resp := runWindowCreate(t, api, windowCreatePlan(maintenanceWindowFromAPI(want)))
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	var body apiMaintenanceWindow
	if err := json.Unmarshal(api.posts[0]["maintenanceWindow"], &body); err != nil {
		t.Fatalf("maintenanceWindow: %v", err)
	}
	if body.StartTime != "03:00" || len(body.Days) != 1 || body.Days[0] != "wed" {
		t.Errorf("body = %+v", body)
	}
}

func TestCreate_WindowRefusalSaysTheClusterWasNotCreated(t *testing.T) {
	api := &tagsAPI{t: t, refuseWindow: true}
	resp := runWindowCreate(t, api, windowCreatePlan(maintenanceWindowFromAPI(window("03:00", "wed"))))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"The cluster was not created", "old cluster is already destroyed", "credential that holds", "access policy"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
	// A create has no stored value to restore.
	if strings.Contains(detail, "stored value") || strings.Contains(detail, "nothing in it was applied") {
		t.Errorf("detail %q carries the update's remedy", detail)
	}
}

func TestRead_WindowDriftAndAnOlderServer(t *testing.T) {
	stored := window("00:00", "thu")
	for name, tc := range map[string]struct {
		api, want *apiMaintenanceWindow
	}{
		"drift is read back":          {window("05:00", "fri"), window("05:00", "fri")},
		"an absent field keeps prior": {nil, stored},
	} {
		t.Run(name, func(t *testing.T) {
			api := &tagsAPI{t: t, window: tc.api}
			r, stop := api.resource(nil)
			defer stop()
			m := stateModel()
			m.MaintenanceWindow = maintenanceWindowFromAPI(stored)
			state := buildState(t, m)
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read: %v", resp.Diagnostics.Errors())
			}
			var got KubernetesClusterModel
			resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
			if !got.MaintenanceWindow.Equal(maintenanceWindowFromAPI(tc.want)) {
				t.Errorf("window = %v, want %+v", got.MaintenanceWindow, tc.want)
			}
		})
	}
}

// A request never carries a null days list.
func TestMaintenanceWindowToAPI_DaysAreNeverNull(t *testing.T) {
	b, err := json.Marshal(maintenanceWindowToAPI(maintenanceWindowFromAPI(&apiMaintenanceWindow{})))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"days":[]`) {
		t.Errorf("body = %s", b)
	}
}

// The refusal as servicekit's authz middleware really renders it: the message does not
// have to name the operation; required_permission does.
func TestUpdate_WindowRefusalOnTheRealEnvelope(t *testing.T) {
	api := &tagsAPI{t: t, window: window("00:00", "thu"), refuseWindow: true, refusalBody: map[string]string{
		"code":                "insufficient_permission",
		"message":             "your API key's access policy does not permit it",
		"required_permission": maintenanceWindowOperation,
	}}
	stateM := stateModel()
	stateM.MaintenanceWindow = maintenanceWindowFromAPI(window("00:00", "thu"))
	planM := stateModel()
	planM.MaintenanceWindow = maintenanceWindowFromAPI(window("03:00", "thu"))
	resp := runWindowUpdate(t, api, stateM, planM)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	d := resp.Diagnostics.Errors()[0]
	if d.Summary() != "Not Allowed To Set The Maintenance Window" {
		t.Errorf("summary = %q, want the window's own", d.Summary())
	}
	if !strings.Contains(d.Detail(), "(required permission: "+maintenanceWindowOperation+")") {
		t.Errorf("detail %q lacks the demanded permission", d.Detail())
	}
}

func TestModifyPlan_WarnsOfAReplacementWithAConfiguredWindow(t *testing.T) {
	configured := maintenanceWindowFromAPI(window("03:00", "wed"))
	const summary = "Replacing this cluster: creating it again needs the maintenance window operation"
	for _, tc := range []struct {
		name     string
		config   types.Object
		replace  bool
		wantWarn bool
	}{
		{"replacement, window configured", configured, true, true},
		{"replacement, window left to the server, plan still holds one", nullMaintenanceWindow(), true, false},
		{"no replacement, window configured", configured, false, false},
		{"replacement, window from an unknown expression", types.ObjectUnknown(maintenanceWindowAttrTypes), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &tagsAPI{t: t}
			r, stop := api.resource(nil)
			defer stop()
			stateM := stateModel()
			stateM.MaintenanceWindow = configured
			planM := stateModel()
			planM.MaintenanceWindow = configured
			if tc.replace {
				planM.SubnetID = types.StringValue("sn-2")
			}
			configM := planM
			configM.MaintenanceWindow = tc.config
			plan := buildPlan(t, planM)
			req := resource.ModifyPlanRequest{State: buildState(t, stateM), Plan: plan, Config: tfsdk.Config(buildPlan(t, configM))}
			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), req, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("modify plan: %v", resp.Diagnostics.Errors())
			}
			got := false
			for _, w := range resp.Diagnostics.Warnings() {
				if w.Summary() == summary {
					got = true
					if !strings.Contains(w.Detail(), "destroyed first") || !strings.Contains(w.Detail(), "access policy") {
						t.Errorf("detail = %q", w.Detail())
					}
				}
			}
			if got != tc.wantWarn {
				t.Errorf("warned = %v, want %v: %v", got, tc.wantWarn, resp.Diagnostics.Warnings())
			}
		})
	}
}

// A fresh create has nothing to destroy.
func TestModifyPlan_NoReplacementWarningOnAFreshCreate(t *testing.T) {
	api := &tagsAPI{t: t}
	r, stop := api.resource(nil)
	defer stop()
	planM := stateModel()
	planM.MaintenanceWindow = maintenanceWindowFromAPI(window("03:00", "wed"))
	plan := buildPlan(t, planM)
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: emptyState(t), Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	for _, w := range resp.Diagnostics.Warnings() {
		if strings.Contains(w.Summary(), "maintenance window") {
			t.Errorf("warned on a create: %s", w.Summary())
		}
	}
}
