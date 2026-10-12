package kubernetes_cluster

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const (
	tfVolToken  = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12" // pragma: allowlist secret
	tfVolDelete = `{"volumeId":"11111111-1111-4111-8111-111111111111","name":"pvc-scratch","sizeGb":10,"pvcNamespace":"default","pvcName":"scratch","reclaimPolicy":"Delete","disposition":"delete"}`
	tfVolRetain = `{"volumeId":"22222222-2222-4222-8222-222222222222","name":"pvc-db","sizeGb":50,"pvcNamespace":"prod","pvcName":"db-data","reclaimPolicy":"Retain","disposition":"ask"}`
	tfVolUnknwn = `{"volumeId":"33333333-3333-4333-8333-333333333333","name":"pvc-old","sizeGb":5,"pvcNamespace":"","pvcName":"","reclaimPolicy":"Unknown","disposition":"ask"}`
)

type tfVolAPI struct {
	volumesStatus int // 0 = 200
	volumes       []string
	conflict      string // non-empty: DELETE answers 409 with it
	clusterStatus string // GET cluster status after the delete; "" = 404
	initialStatus string // GET cluster status before any delete; "" = running, "gone" = 404
	preflights    int

	mu      sync.Mutex
	deletes []url.Values
}

func (a *tfVolAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const base = "/v1/tenants/t-1/kubernetes-clusters/c-1"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/persistent-volumes":
			a.mu.Lock()
			a.preflights++
			a.mu.Unlock()
			if a.volumesStatus != 0 {
				w.WriteHeader(a.volumesStatus)
				_, _ = io.WriteString(w, `{"code":"not_found","message":"no route"}`)
				return
			}
			_, _ = io.WriteString(w, `{"volumes":[`+strings.Join(a.volumes, ",")+`],"retainedSetToken":"`+tfVolToken+`","reportedAt":"2026-10-12T10:00:00Z"}`)
		case r.Method == http.MethodDelete && r.URL.Path == base:
			a.mu.Lock()
			a.deletes = append(a.deletes, r.URL.Query())
			a.mu.Unlock()
			if a.conflict != "" {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, a.conflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == base:
			a.mu.Lock()
			before := len(a.deletes) == 0
			a.mu.Unlock()
			if before && a.initialStatus != "gone" {
				c := runningCluster()
				if a.initialStatus != "" {
					c.Status = a.initialStatus
				}
				// A cluster already deleting converges to deleted on the next read.
				if a.initialStatus == statusDeleting {
					a.mu.Lock()
					a.initialStatus = statusDeleted
					a.mu.Unlock()
				}
				writeJSON(t, w, c)
				return
			}
			if a.clusterStatus == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"code":"not_found","message":"gone"}`)
				return
			}
			c := runningCluster()
			c.Status = a.clusterStatus
			writeJSON(t, w, c)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (a *tfVolAPI) deleteCalls() []url.Values {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]url.Values(nil), a.deletes...)
}

func runVolDelete(t *testing.T, api *tfVolAPI, choice types.String) resource.DeleteResponse {
	t.Helper()
	srv := api.server(t)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)
	m := stateModel()
	m.RetainedVolumesOnDestroy = choice
	state := buildState(t, m)
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	return resp
}

func TestDeleteRetainedVolumesUnsetFailsListingThem(t *testing.T) {
	api := &tfVolAPI{volumes: []string{tfVolDelete, tfVolRetain, tfVolUnknwn}, clusterStatus: statusDeleted}
	resp := runVolDelete(t, api, types.StringNull())
	if !resp.Diagnostics.HasError() {
		t.Fatal("destroy with retained volumes and no retained_volumes_on_destroy must fail")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"22222222-2222-4222-8222-222222222222", "pvc-db", "50 GB", "prod/db-data", "Retain",
		"33333333-3333-4333-8333-333333333333", "Unknown", "retained_volumes_on_destroy", "Nothing was deleted"} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnostic does not name %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "11111111-1111-4111-8111-111111111111") {
		t.Errorf("a Delete-policy volume is listed as retained:\n%s", detail)
	}
	if len(api.deleteCalls()) != 0 {
		t.Fatal("no DELETE may be sent")
	}
}

func TestDeleteRetainedVolumesChoice(t *testing.T) {
	for _, tc := range []struct {
		choice    string
		wantToken string
	}{{"keep", ""}, {"delete", tfVolToken}} {
		t.Run(tc.choice, func(t *testing.T) {
			api := &tfVolAPI{volumes: []string{tfVolDelete, tfVolRetain}, clusterStatus: statusDeleted}
			resp := runVolDelete(t, api, types.StringValue(tc.choice))
			if resp.Diagnostics.HasError() {
				t.Fatalf("delete failed: %v", resp.Diagnostics.Errors())
			}
			d := api.deleteCalls()
			if len(d) != 1 {
				t.Fatalf("want one DELETE, got %d", len(d))
			}
			if d[0].Get("retainedVolumes") != tc.choice || d[0].Get("retainedSetToken") != tc.wantToken {
				t.Fatalf("query = %v", d[0])
			}
		})
	}
}

func TestDeleteWithoutRetainedVolumesIgnoresTheSetting(t *testing.T) {
	// Nothing to ask about: no parameters, whatever the setting says.
	api := &tfVolAPI{volumes: []string{tfVolDelete}, clusterStatus: statusDeleted}
	resp := runVolDelete(t, api, types.StringValue("delete"))
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", resp.Diagnostics.Errors())
	}
	if d := api.deleteCalls(); len(d) != 1 || len(d[0]) != 0 {
		t.Fatalf("want one DELETE with no query, got %v", d)
	}
}

func TestDeleteConflictListsTheFreshSet(t *testing.T) {
	api := &tfVolAPI{volumes: []string{tfVolRetain}, clusterStatus: statusDeleted,
		conflict: `{"code":"retained_volumes_decision_required","message":"decide","details":{"volumes":[` + tfVolRetain +
			`,{"volumeId":"44444444-4444-4444-8444-444444444444","name":"pvc-new","sizeGb":1,"pvcNamespace":"a","pvcName":"b","reclaimPolicy":"Retain","disposition":"ask"}],"retainedSetToken":"cd","reportedAt":null}}`}
	resp := runVolDelete(t, api, types.StringValue("delete"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 409 must fail the destroy")
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "44444444-4444-4444-8444-444444444444") ||
		!strings.Contains(detail, "Nothing was deleted") {
		t.Fatalf("diagnostic does not list the fresh set:\n%s", detail)
	}
}

func TestDeletePreflight404OnALiveClusterIsAnError(t *testing.T) {
	// A platform that does not serve the volume check answers 404 too; the live
	// cluster must NOT be dropped from state as "already gone".
	api := &tfVolAPI{volumesStatus: http.StatusNotFound, clusterStatus: statusRunning}
	resp := runVolDelete(t, api, types.StringNull())
	if !resp.Diagnostics.HasError() {
		t.Fatal("a preflight 404 on a live cluster must fail the destroy")
	}
	if len(api.deleteCalls()) != 0 {
		t.Fatal("no DELETE may be sent")
	}
}

func TestDeleteOfAGoneClusterSucceeds(t *testing.T) {
	api := &tfVolAPI{initialStatus: "gone"}
	resp := runVolDelete(t, api, types.StringNull())
	if resp.Diagnostics.HasError() {
		t.Fatalf("a gone cluster must destroy cleanly: %v", resp.Diagnostics.Errors())
	}
	if len(api.deleteCalls()) != 0 || api.preflights != 0 {
		t.Fatal("no preflight or DELETE needed for a gone cluster")
	}
}

// A repeat destroy of a cluster already being deleted: no preflight, no DELETE (the
// first delete's choice stands), just the wait — even with Retain volumes and the
// setting unset.
func TestDeleteOfADeletingClusterOnlyWaits(t *testing.T) {
	api := &tfVolAPI{initialStatus: statusDeleting, volumes: []string{tfVolRetain}}
	resp := runVolDelete(t, api, types.StringNull())
	if resp.Diagnostics.HasError() {
		t.Fatalf("repeat destroy must succeed: %v", resp.Diagnostics.Errors())
	}
	if len(api.deleteCalls()) != 0 || api.preflights != 0 {
		t.Fatalf("want no preflight and no DELETE, got %d preflights, %d deletes", api.preflights, len(api.deleteCalls()))
	}
}

func TestDeleteInProgressConflictWaits(t *testing.T) {
	api := &tfVolAPI{volumes: []string{tfVolRetain}, clusterStatus: statusDeleted,
		conflict: `{"code":"delete_in_progress","message":"already deleting"}`}
	resp := runVolDelete(t, api, types.StringValue("keep"))
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete_in_progress must wait, not fail: %v", resp.Diagnostics.Errors())
	}
}

func TestDeleteTooManyVolumes(t *testing.T) {
	api := &tfVolAPI{volumes: []string{tfVolRetain}, clusterStatus: statusDeleted,
		conflict: `{"code":"too_many_cluster_volumes","message":"too many","details":{"limit":500,"count":612}}`}
	resp := runVolDelete(t, api, types.StringValue("delete"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("too_many_cluster_volumes must fail")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"612", "500", "Delete some PVCs", "keep"} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnostic does not say %q:\n%s", want, detail)
		}
	}
}

func TestModifyPlanWarnsOnReplaceWithoutRetainedChoice(t *testing.T) {
	ctx := context.Background()
	mp := NewResource().(resource.ResourceWithModifyPlan)
	for name, tc := range map[string]struct {
		choice   types.String
		mutate   func(*KubernetesClusterModel)
		wantWarn bool
	}{
		"replace, unset":       {types.StringNull(), func(m *KubernetesClusterModel) { m.SubnetID = types.StringValue("sn-2") }, true},
		"replace pool, unset":  {types.StringNull(), func(m *KubernetesClusterModel) { m.InitialNodePool.FlavorID = types.StringValue("big") }, true},
		"replace, set":         {types.StringValue("keep"), func(m *KubernetesClusterModel) { m.SubnetID = types.StringValue("sn-2") }, false},
		"in-place, unset":      {types.StringNull(), func(m *KubernetesClusterModel) { m.Name = types.StringValue("renamed") }, false},
		"no change, unset":     {types.StringNull(), func(*KubernetesClusterModel) {}, false},
		"version unknown, set": {types.StringValue("delete"), func(m *KubernetesClusterModel) { m.Version = types.StringUnknown() }, false},
	} {
		t.Run(name, func(t *testing.T) {
			sm := stateModel()
			sm.PublicIPID = types.StringNull()
			sm.RetainedVolumesOnDestroy = tc.choice
			state := buildState(t, sm)
			pm := stateModel()
			pm.PublicIPID = types.StringNull()
			pm.RetainedVolumesOnDestroy = tc.choice
			tc.mutate(&pm)
			plan := buildPlan(t, pm)
			resp := &resource.ModifyPlanResponse{Plan: plan}
			mp.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan, State: state}, resp)
			warned := false
			for _, d := range resp.Diagnostics.Warnings() {
				if strings.Contains(d.Summary(), "retained_volumes_on_destroy") {
					warned = true
				}
			}
			if warned != tc.wantWarn {
				t.Fatalf("warned = %v, want %v (%v)", warned, tc.wantWarn, resp.Diagnostics)
			}
		})
	}
}

// Changing retained_volumes_on_destroy alone is recorded with NO API call.
func TestUpdateOnlyRetainedVolumesOnDestroyMakesNoAPICall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	planModel := stateModel()
	planModel.RetainedVolumesOnDestroy = types.StringValue("keep")
	plan := buildPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	var got KubernetesClusterModel
	resp.State.Get(context.Background(), &got)
	if got.RetainedVolumesOnDestroy.ValueString() != "keep" {
		t.Fatalf("retained_volumes_on_destroy = %v", got.RetainedVolumesOnDestroy)
	}
	if got.Name.ValueString() != "test-cluster" || got.Kubeconfig.ValueString() != "prior-kubeconfig" {
		t.Fatalf("the rest of state must be carried unchanged: %+v", got)
	}
}

func TestSchemaRetainedVolumesOnDestroy(t *testing.T) {
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	a, ok := resp.Schema.Attributes["retained_volumes_on_destroy"].(schema.StringAttribute)
	if !ok {
		t.Fatal("retained_volumes_on_destroy missing or not a string")
	}
	if !a.Optional || a.Computed || a.Required || a.Default != nil || len(a.PlanModifiers) != 0 {
		t.Fatalf("want Optional, not Computed, no default, no plan modifiers (no RequiresReplace): %+v", a)
	}
	if len(a.Validators) != 1 {
		t.Fatalf("want the keep|delete validator, got %d", len(a.Validators))
	}
}
