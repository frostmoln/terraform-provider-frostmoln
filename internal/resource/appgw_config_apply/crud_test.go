package appgw_config_apply

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const (
	gwPath    = "/v1/tenants/t-1/application-gateways/agw-1"
	applyPath = gwPath + "/config/apply"
)

func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m Model) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func model(t *testing.T) Model {
	t.Helper()
	revs, d := types.MapValueFrom(context.Background(), types.StringType,
		map[string]string{
			"listener.https": "2026-08-29T10:00:00.123456Z",
			"route.app":      "2026-08-29T10:00:01.654321Z",
		})
	if d.HasError() {
		t.Fatalf("revisions: %v", d.Errors())
	}
	return Model{GatewayID: types.StringValue("agw-1"), Triggers: revs}
}

func shrinkWaits(t *testing.T) {
	t.Helper()
	oi, ot := applyPollInterval, applyTimeout
	applyPollInterval, applyTimeout = 5*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { applyPollInterval, applyTimeout = oi, ot })
}

func serve(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

func create(t *testing.T, c *client.Client) resource.CreateResponse {
	t.Helper()
	r := &applyResource{client: c}
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, model(t))}, &resp)
	return resp
}

// 🔴 THE WHOLE POINT: a 202 is not convergence. The appliance validates, swaps,
// reloads and probes itself before it answers, so returning on the 202 would
// report success while the gateway was still serving the old configuration —
// the exact defect this resource exists to fix, moved one step along.
func TestApplyWaitsForTheApplianceToAcknowledge(t *testing.T) {
	shrinkWaits(t)
	reads := 0
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == applyPath && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":9,"sha256":"abc"}`))
		case r.URL.Path == gwPath && r.Method == http.MethodGet:
			reads++
			// Still working the first two polls, then acknowledged.
			if reads < 3 {
				_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":9,"configStatus":"applying"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":9,"configRevision":9,` +
				`"configStatus":"applied","configAppliedAt":"2026-08-29T10:00:00Z"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	resp := create(t, c)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if reads < 3 {
		t.Errorf("returned after %d reads; it did not wait for the verdict", reads)
	}

	var got Model
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	if got.Status.ValueString() != "applied" {
		t.Errorf("status = %q, want applied", got.Status.ValueString())
	}
	if got.Revision.ValueInt64() != 9 {
		t.Errorf("revision = %d, want 9", got.Revision.ValueInt64())
	}
}

// A configuration the proxy will not load must FAIL the terraform apply, and it
// must fail with the proxy's own sentence. A `terraform apply` that succeeds
// while the gateway serves something else is worse than one that fails.
func TestApplyFailsWhenTheApplianceRefuses(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == applyPath {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":4,"sha256":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":4,"configRevision":3,` +
			`"configStatus":"failed","configDetail":"[ALERT] parsing [/etc/haproxy/haproxy.cfg:12] : unknown keyword"}`))
	})

	resp := create(t, c)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused configuration reported success")
	}
	joined := resp.Diagnostics.Errors()[0].Summary() + " " + resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(joined, "unknown keyword") {
		t.Errorf("the proxy's own words are missing from the error: %q", joined)
	}
	if !strings.Contains(joined, "still serving its previous configuration") {
		t.Errorf("the error does not say what the gateway is doing now: %q", joined)
	}
}

// An appliance that never answers must not hang a terraform apply for ever, and
// the timeout must say what was dispatched and what was acknowledged.
func TestApplyTimesOutWithoutAVerdict(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == applyPath {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":5,"sha256":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":5,"configStatus":"applying"}`))
	})

	resp := create(t, c)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a never-answering appliance reported success")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(strings.ToLower(d), "revision 5") {
		t.Errorf("timeout does not name the dispatched revision: %q", d)
	}
}

// A newer acknowledged revision is convergence, not failure: a concurrent
// authoring write can be dispatched by a later apply while this one is in
// flight, and demanding equality would fail an apply that in fact converged.
func TestApplyAcceptsANewerAcknowledgedRevision(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == applyPath {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":6,"sha256":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":8,"configRevision":8,"configStatus":"applied"}`))
	})

	if resp := create(t, c); resp.Diagnostics.HasError() {
		t.Fatalf("a newer acknowledged revision was treated as a failure: %v", resp.Diagnostics.Errors())
	}
}

func stateOf(t *testing.T, m Model) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: schemaOf(t).Schema}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return st
}

// 🔴 THE CRITICAL THIS RESOURCE SHIPPED WITHOUT. `revision`, `id`, `status` and
// `applied_at` are computed with no config counterpart, so Terraform's
// proposed-new plan reuses the PRIOR state value. Without ModifyPlan the plan
// carries revision 7, the apply returns 8, and core rejects it with "Provider
// produced inconsistent result after apply" — on an update, which is the
// resource's primary path and the whole reason it exists.
func TestModifyPlanMarksOutputsUnknownWhenTriggersMove(t *testing.T) {
	prior := model(t)
	prior.Revision = types.Int64Value(7)
	prior.ID = types.StringValue("agw-1:7")
	prior.Status = types.StringValue("applied")
	prior.SHA256 = types.StringValue("old")
	prior.AppliedAt = types.StringValue("2026-08-29T09:00:00Z")

	moved := prior
	tr, d := types.MapValueFrom(context.Background(), types.StringType,
		map[string]string{"listener.https": "2026-08-29T11:00:00.000001Z"})
	if d.HasError() {
		t.Fatalf("triggers: %v", d.Errors())
	}
	moved.Triggers = tr

	r := &applyResource{}
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan(stateOf(t, moved))}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan:  tfsdk.Plan(stateOf(t, moved)),
		State: stateOf(t, prior),
	}, &resp)

	var planned Model
	if d := resp.Plan.Get(context.Background(), &planned); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	for name, unknown := range map[string]bool{
		"revision":   planned.Revision.IsUnknown(),
		"id":         planned.ID.IsUnknown(),
		"status":     planned.Status.IsUnknown(),
		"sha256":     planned.SHA256.IsUnknown(),
		"applied_at": planned.AppliedAt.IsUnknown(),
	} {
		if !unknown {
			t.Errorf("%s stayed known in the plan; the apply will fail with "+
				"\"Provider produced inconsistent result after apply\"", name)
		}
	}
}

// The delete hole: a child removed from the configuration updates THIS resource
// first, dispatching a generation that still contains the child, and only then
// is the child destroyed — bumping the generation again with nothing to dispatch
// it. triggers now matches state, so without the gateway read the next plan is
// silent and the deleted child stays served for ever.
func TestModifyPlanReplansWhenTheGatewayIsUnconverged(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":12,"configRevision":11,"configStatus":"applied"}`))
	})

	same := model(t)
	same.Revision = types.Int64Value(11)
	same.ID = types.StringValue("agw-1:11")
	same.Status = types.StringValue("applied")

	r := &applyResource{client: c}
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan(stateOf(t, same))}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan:  tfsdk.Plan(stateOf(t, same)),
		State: stateOf(t, same),
	}, &resp)

	var planned Model
	if d := resp.Plan.Get(context.Background(), &planned); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	if !planned.Revision.IsUnknown() {
		t.Error("an unconverged gateway planned no change; a deleted child would stay served")
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("re-dispatching without saying why")
	}
}

// Converged and unchanged must plan nothing, or every plan re-applies.
func TestModifyPlanIsQuietWhenConverged(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":11,"configRevision":11,"configStatus":"applied"}`))
	})
	same := model(t)
	same.Revision = types.Int64Value(11)
	same.Status = types.StringValue("applied")

	r := &applyResource{client: c}
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan(stateOf(t, same))}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan: tfsdk.Plan(stateOf(t, same)), State: stateOf(t, same),
	}, &resp)

	var planned Model
	_ = resp.Plan.Get(context.Background(), &planned)
	if planned.Revision.IsUnknown() {
		t.Error("a converged gateway planned a re-apply")
	}
}

// `unknown` is TERMINAL and deliberately weaker than `failed`: the reaper writes
// it when no verdict arrived, and nothing will ever move it. Waiting for the
// ceiling would burn the full timeout to print the wrong message.
func TestApplyTreatsUnknownAsTerminal(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == applyPath {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":3,"sha256":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":3,"configStatus":"unknown",` +
			`"configDetail":"no verdict was received before the apply was abandoned"}`))
	})

	start := time.Now()
	resp := create(t, c)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an abandoned apply reported success")
	}
	if time.Since(start) > 150*time.Millisecond {
		t.Error("it waited for the ceiling instead of treating unknown as terminal")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(d, "NOTHING about what the gateway is serving") {
		t.Errorf("unknown was reported as if it were a refusal: %q", d)
	}
}

// A transient 409 is self-clearing by the server's own account; failing on it
// wedges the workspace.
func TestApplyRetriesATransientConflict(t *testing.T) {
	shrinkWaits(t)
	posts := 0
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == applyPath {
			posts++
			if posts == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"CONFIG_CHANGED_DURING_RENDER","message":"apply again to send the newer one"}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":4,"sha256":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":4,"configRevision":4,"configStatus":"applied"}`))
	})

	if resp := create(t, c); resp.Diagnostics.HasError() {
		t.Fatalf("a self-clearing 409 was treated as fatal: %v", resp.Diagnostics.Errors())
	}
	if posts < 2 {
		t.Errorf("posted %d times; the transient conflict was not retried", posts)
	}
}

// A dispatched apply must survive a wait failure in state: losing it re-POSTs
// into a live attempt and collects 409s until the reaper fires hours later.
func TestApplyKeepsStateWhenTheVerdictFails(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == applyPath {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operationId":"op-1","revision":5,"sha256":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"agw-1","configGeneration":5,"configRevision":4,` +
			`"configStatus":"failed","configDetail":"unknown keyword"}`))
	})

	resp := create(t, c)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a refusal reported success")
	}
	var got Model
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	if got.GatewayID.ValueString() != "agw-1" {
		t.Error("the dispatched apply was lost from state; the next run will collect a 409")
	}
}
