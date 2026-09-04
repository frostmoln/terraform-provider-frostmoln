package appgw_waf_policy_attachment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m AttachmentModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m AttachmentModel) tfsdk.State {
	t.Helper()
	p := schemaOf(t)
	s := tfsdk.State{Schema: p.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return s
}

func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: schemaOf(t).Schema}
}

func importStateValue(t *testing.T) tfsdk.State {
	t.Helper()
	s := schemaOf(t).Schema
	obj := s.Type().TerraformType(context.Background()).(tftypes.Object)
	attrs := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(obj, attrs)}
}

func serve(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

const base = "/v1/tenants/t-1/application-gateways/agw-1"

// seenRequest is one request the fake API took.
type seenRequest struct {
	method string
	path   string
	body   map[string]any
}

// api serves the policy read plus the three attachment endpoints, and records
// every write so a test can assert that a REFUSED attachment sent nothing.
//
// 🔴 THE FAKE MODELS THE SERVER'S CONSTRAINTS, NOT THIS CLIENT'S HABITS.
// Two of them are load-bearing and a lazier fake would hide both:
//
//   - the attach body's policyId is `binding:"required"` on the server, so a
//     body under any other name is a 400 -- answered here as a 400, not waved
//     through;
//   - attach answers 202 WITH the policy and detach answers 204 with nothing.
//     A fake that returned 204 to both would let a client asserting the wrong
//     status pass here and fail in production.
func api(t *testing.T, policy apiPolicy, attached string) (*client.Client, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		rec := seenRequest{method: r.Method, path: r.URL.Path}
		_ = json.NewDecoder(r.Body).Decode(&rec.body)
		if r.Method != http.MethodGet {
			seen = append(seen, rec)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/waf-policies/"):
			_ = json.NewEncoder(w).Encode(policy)
		case r.Method == http.MethodGet:
			// The gateway, listener or route, reporting what it carries.
			_ = json.NewEncoder(w).Encode(apiAttachee{WAFPolicyID: attached})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			if _, ok := rec.body["policyId"].(string); !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"BAD_REQUEST","message":"policyId is required"}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(policy)
		}
	})
	return c, &seen
}

func gatewayPolicy() apiPolicy {
	return apiPolicy{ID: "wp-gw", Scope: scopeGateway, Mode: "block", EffectiveMode: "block"}
}

// overlayPolicy is the shape that makes a client lie: mode "inherit", which is
// neither "detect" nor "block", resolved by the server to "block".
func overlayPolicy() apiPolicy {
	return apiPolicy{ID: "wp-ov", Scope: scopeOverlay, Mode: "inherit", EffectiveMode: "block"}
}

func model(policyID string) AttachmentModel {
	return AttachmentModel{
		GatewayID: types.StringValue("agw-1"),
		PolicyID:  types.StringValue(policyID),
	}
}

// TestAttachReachesTheRightEndpointAtEachLevel.
//
// The three levels differ only in the path, and the path ends in the SINGULAR
// `waf-policy` -- a client that reached `waf-policies` would land a PUT on the
// policy collection. The body carries exactly the policy id, under the name the
// rest of the appgw surface already uses for this referent.
func TestAttachReachesTheRightEndpointAtEachLevel(t *testing.T) {
	cases := []struct {
		name     string
		policy   apiPolicy
		build    func() AttachmentModel
		wantPath string
		wantID   string
	}{
		{
			name: "gateway", policy: gatewayPolicy(),
			build:    func() AttachmentModel { return model("wp-gw") },
			wantPath: base + "/waf-policy",
			wantID:   "agw-1",
		},
		{
			name: "listener", policy: overlayPolicy(),
			build: func() AttachmentModel {
				m := model("wp-ov")
				m.ListenerID = types.StringValue("l-1")
				return m
			},
			wantPath: base + "/listeners/l-1/waf-policy",
			wantID:   "agw-1/l-1",
		},
		{
			name: "route", policy: overlayPolicy(),
			build: func() AttachmentModel {
				m := model("wp-ov")
				m.ListenerID = types.StringValue("l-1")
				m.RouteID = types.StringValue("r-7")
				return m
			},
			wantPath: base + "/listeners/l-1/routes/r-7/waf-policy",
			wantID:   "agw-1/l-1/r-7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := api(t, tc.policy, tc.policy.ID)
			ar := &attachmentResource{client: c}

			resp := resource.CreateResponse{State: emptyState(t)}
			ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, tc.build())}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("create: %v", resp.Diagnostics.Errors())
			}
			if len(*seen) != 1 {
				t.Fatalf("expected exactly one write, got %+v", *seen)
			}
			got := (*seen)[0]
			if got.method != http.MethodPut || got.path != tc.wantPath {
				t.Fatalf("attach went to %s %s, want PUT %s", got.method, got.path, tc.wantPath)
			}
			// 🔴 policyId, not wafPolicyId. `binding:"required"` on the server
			// makes the wrong name a 400 at every level rather than a field
			// quietly ignored, and wafPolicyId is the plausible wrong answer
			// because that is what the attachee reports it back as.
			if got.body["policyId"] != tc.policy.ID {
				t.Fatalf("attach body = %v, want policyId %q", got.body, tc.policy.ID)
			}
			if _, wrong := got.body["wafPolicyId"]; wrong {
				t.Fatalf("the attach body uses wafPolicyId; the server requires policyId: %v", got.body)
			}
			if len(got.body) != 1 {
				t.Fatalf("the attach body carries more than the policy id: %v", got.body)
			}

			var state AttachmentModel
			resp.State.Get(context.Background(), &state)
			if state.ID.ValueString() != tc.wantID {
				t.Errorf("id = %q, want %q", state.ID.ValueString(), tc.wantID)
			}

			// 🔴 THE EFFECTIVE MODE, NOT THE AUTHORED ONE. Both fixtures are
			// enforcing block; the overlay's `mode` says "inherit".
			if state.EffectiveMode.ValueString() != "block" {
				t.Fatalf("effective_mode = %q, want block -- this attachment is refusing requests",
					state.EffectiveMode.ValueString())
			}

			delResp := resource.DeleteResponse{State: stateOf(t, state)}
			ar.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, state)}, &delResp)
			if delResp.Diagnostics.HasError() {
				t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
			}
			last := (*seen)[len(*seen)-1]
			if last.method != http.MethodDelete || last.path != tc.wantPath {
				t.Fatalf("detach went to %s %s, want DELETE %s", last.method, last.path, tc.wantPath)
			}
		})
	}
}

// TestAttachRefusesAScopeMismatch.
//
// A gateway policy carries the managed ruleset; an overlay is not compiled with
// it. Attaching an overlay to the gateway would leave the gateway with no
// managed protection at all -- so the refusal happens here, saying what it
// would have cost, and NOTHING is sent.
func TestAttachRefusesAScopeMismatch(t *testing.T) {
	t.Run("a gateway policy on a listener", func(t *testing.T) {
		c, seen := api(t, gatewayPolicy(), "")
		ar := &attachmentResource{client: c}
		m := model("wp-gw")
		m.ListenerID = types.StringValue("l-1")

		resp := resource.CreateResponse{State: emptyState(t)}
		ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("a gateway-scoped policy was attached to a listener")
		}
		if !strings.Contains(joinDiags(resp.Diagnostics.Errors()), "overlay") {
			t.Errorf("the refusal does not name the scope a listener takes: %v", resp.Diagnostics.Errors())
		}
		if len(*seen) != 0 {
			t.Fatalf("the refused attach still wrote: %+v", *seen)
		}
	})

	t.Run("an overlay policy on the gateway", func(t *testing.T) {
		c, seen := api(t, overlayPolicy(), "")
		ar := &attachmentResource{client: c}

		resp := resource.CreateResponse{State: emptyState(t)}
		ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, model("wp-ov"))}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("an overlay-scoped policy was attached to the gateway")
		}
		if !strings.Contains(joinDiags(resp.Diagnostics.Errors()), "no managed protection") {
			t.Errorf("the refusal does not say what this would cost: %v", resp.Diagnostics.Errors())
		}
		if len(*seen) != 0 {
			t.Fatalf("the refused attach still wrote: %+v", *seen)
		}
	})
}

// TestReadRemovesTheAttachmentWhenNothingIsAttached.
//
// The attachee reports which policy it carries; an empty value means someone
// detached it out of band. Removing this from state makes the next plan
// re-attach, which is the direction that restores the protection the
// configuration asks for -- reporting agreement would leave the gateway
// unprotected and Terraform saying "no changes".
func TestReadRemovesTheAttachmentWhenNothingIsAttached(t *testing.T) {
	c, _ := api(t, gatewayPolicy(), "") // nothing attached any more
	ar := &attachmentResource{client: c}

	state := model("wp-gw")
	state.ID = types.StringValue("agw-1")
	state.Scope = types.StringValue(scopeGateway)
	state.EffectiveMode = types.StringValue("block")

	resp := resource.ReadResponse{State: stateOf(t, state)}
	ar.Read(context.Background(), resource.ReadRequest{State: stateOf(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("an attachment that is no longer there stayed in state, so the next plan " +
			"reports agreement with an unprotected gateway")
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("the out-of-band detach was removed from state silently")
	}
}

// TestReadPicksUpAnOutOfBandReAttach. Somebody pointing the gateway at a
// different policy is drift this resource has to show, not absorb.
func TestReadPicksUpAnOutOfBandReAttach(t *testing.T) {
	other := gatewayPolicy()
	other.ID = "wp-other"
	c, _ := api(t, other, "wp-other")
	ar := &attachmentResource{client: c}

	state := model("wp-gw")
	state.ID = types.StringValue("agw-1")

	resp := resource.ReadResponse{State: stateOf(t, state)}
	ar.Read(context.Background(), resource.ReadRequest{State: stateOf(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var got AttachmentModel
	resp.State.Get(context.Background(), &got)
	if got.PolicyID.ValueString() != "wp-other" {
		t.Fatalf("policy_id = %q; the refresh did not pick up the policy actually attached",
			got.PolicyID.ValueString())
	}
}

// TestDeletingTheGatewayAttachmentWarnsAboutInheritingOverlays.
//
// 🔴 THIS DETACH IS NOT A LOCAL ACT. Every overlay whose mode is "inherit"
// resolves against the gateway policy, so with it gone they fall back to detect
// and STOP refusing requests. A destroy that said only "detached" would be the
// quietest way in this provider to turn off a firewall.
func TestDeletingTheGatewayAttachmentWarnsAboutInheritingOverlays(t *testing.T) {
	c, _ := api(t, gatewayPolicy(), "wp-gw")
	ar := &attachmentResource{client: c}

	state := model("wp-gw")
	state.ID = types.StringValue("agw-1")
	resp := resource.DeleteResponse{State: stateOf(t, state)}
	ar.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics.Errors())
	}
	if !strings.Contains(joinWarnings(resp.Diagnostics), "inherit") {
		t.Fatalf("detaching the gateway policy did not say that inheriting overlays stop "+
			"blocking: %v", resp.Diagnostics.Warnings())
	}

	// A LISTENER detach is local, and must not carry the same alarm -- a
	// warning that fires every time is one nobody reads.
	listener := model("wp-ov")
	listener.ListenerID = types.StringValue("l-1")
	listener.ID = types.StringValue("agw-1/l-1")
	lResp := resource.DeleteResponse{State: stateOf(t, listener)}
	ar.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, listener)}, &lResp)
	if lResp.Diagnostics.WarningsCount() != 0 {
		t.Errorf("detaching one listener's overlay warned about inheritance: %v",
			lResp.Diagnostics.Warnings())
	}
}

// TestValidateConfigRefusesARouteWithoutItsListener. A route id is unique only
// within its listener, so a route on its own names nothing -- and the path it
// would build addresses the listener collection.
func TestValidateConfigRefusesARouteWithoutItsListener(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m AttachmentModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config(p)}, &resp)
		return resp.Diagnostics.HasError()
	}

	orphan := model("wp-ov")
	orphan.RouteID = types.StringValue("r-7")
	if !check(orphan) {
		t.Fatal("route_id without listener_id was accepted")
	}

	full := orphan
	full.ListenerID = types.StringValue("l-1")
	if check(full) {
		t.Error("a route with its listener was refused")
	}
	if check(model("wp-gw")) {
		t.Error("a gateway-level attachment was refused")
	}
}

// TestImportStateAcceptsTheThreeArities. The segment count chooses the level;
// a dot segment is refused at every arity, because an import id becomes a URL
// path segment and ".." there addresses the PARENT with the same method -- for
// a destroy, that is the gateway.
func TestImportStateAcceptsTheThreeArities(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	run := func(id string) (resource.ImportStateResponse, bool) {
		resp := resource.ImportStateResponse{State: importStateValue(t)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &resp)
		return resp, resp.Diagnostics.HasError()
	}

	for _, good := range []string{"agw-1", "agw-1/l-1", "agw-1/l-1/r-7"} {
		resp, err := run(good)
		if err {
			t.Errorf("import id %q was refused: %v", good, resp.Diagnostics.Errors())
			continue
		}
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("gateway_id"), &got); d.HasError() {
			t.Fatalf("read gateway_id: %v", d.Errors())
		}
		if got.ValueString() != "agw-1" {
			t.Errorf("import %q set gateway_id = %q", good, got.ValueString())
		}
	}

	for _, bad := range []string{"", "agw-1/", "/l-1", "agw-1/../r-7", "agw-1/l-1/r-7/extra", ".."} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted", bad)
		}
	}
}

func joinDiags(ds diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString(d.Summary())
		b.WriteString(" ")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

func joinWarnings(ds diag.Diagnostics) string { return joinDiags(ds.Warnings()) }

// TestAttachTreatsThe202AsSuccess.
//
// The three attach handlers answer c.JSON(http.StatusAccepted, p) -- 202, with
// the policy -- and detach answers 204. Neither is 200. A resource that
// insisted on 200, or that required a parseable body from the 204, would fail
// an attach that actually landed and leave Terraform believing the policy is
// unattached while the gateway enforces it.
func TestAttachTreatsThe202AsSuccess(t *testing.T) {
	var attachStatus, detachStatus int
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(gatewayPolicy())
		case http.MethodDelete:
			detachStatus = http.StatusNoContent
			w.WriteHeader(http.StatusNoContent) // no body at all
		default:
			attachStatus = http.StatusAccepted
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(gatewayPolicy())
		}
	})
	ar := &attachmentResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, model("wp-gw"))}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("a 202 attach was reported as a failure: %v", createResp.Diagnostics.Errors())
	}
	if attachStatus != http.StatusAccepted {
		t.Fatal("the attach never reached the server")
	}
	var state AttachmentModel
	createResp.State.Get(context.Background(), &state)
	if state.ID.IsNull() || state.ID.ValueString() == "" {
		t.Fatal("a 202 attach left no id in state, so Terraform does not know it landed")
	}

	delResp := resource.DeleteResponse{State: stateOf(t, state)}
	ar.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, state)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("a 204 detach was reported as a failure: %v", delResp.Diagnostics.Errors())
	}
	if detachStatus != http.StatusNoContent {
		t.Fatal("the detach never reached the server")
	}
}

// TestAttachmentEffectiveModeIsNullWhenItCannotBeResolved.
//
// 🔴 NEVER INVENT A RESOLUTION -- SURFACE UNKNOWN. Every other fixture in this
// file carries a server-resolved effectiveMode, so the fallback branch is
// exactly the one nothing else exercises: an attached overlay whose mode the
// server did not resolve. Handing back the authored token would put the literal
// string "inherit" into an attribute documented as "`mode` with `inherit`
// resolved", so a configuration testing == "block" would take a branch
// confidently on a value that answers nothing.
//
// Dormant against today's server, which always sends effectiveMode on a policy,
// and one server change from live.
func TestAttachmentEffectiveModeIsNullWhenItCannotBeResolved(t *testing.T) {
	unresolved := overlayPolicy()
	unresolved.EffectiveMode = "" // the server did not resolve it
	c, _ := api(t, unresolved, unresolved.ID)
	ar := &attachmentResource{client: c}

	m := model(unresolved.ID)
	m.ListenerID = types.StringValue("l-1")

	resp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	var state AttachmentModel
	resp.State.Get(context.Background(), &state)

	if state.EffectiveMode.ValueString() == "inherit" {
		t.Fatalf("effective_mode carries the authored token; an unresolvable inherit must " +
			"surface as null")
	}
	if !state.EffectiveMode.IsNull() {
		t.Fatalf("effective_mode = %q, want null", state.EffectiveMode.ValueString())
	}
	// The scope check still ran, so surfacing unknown did not cost the guard.
	if state.Scope.ValueString() != scopeOverlay {
		t.Errorf("scope = %q, want overlay", state.Scope.ValueString())
	}

	// An explicit mode needs no resolution and is reported as itself, so the
	// null is specific to inherit rather than to "the server sent no
	// effectiveMode".
	explicit := overlayPolicy()
	explicit.Mode, explicit.EffectiveMode = "detect", ""
	c2, _ := api(t, explicit, explicit.ID)
	ar2 := &attachmentResource{client: c2}
	resp2 := resource.CreateResponse{State: emptyState(t)}
	ar2.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp2)
	if resp2.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp2.Diagnostics.Errors())
	}
	var state2 AttachmentModel
	resp2.State.Get(context.Background(), &state2)
	if state2.EffectiveMode.ValueString() != "detect" {
		t.Fatalf("effective_mode = %v, want detect -- an explicit mode is its own effective mode",
			state2.EffectiveMode)
	}
}

// TestTheAttachmentPlanIsHonestAboutTheEffectiveMode.
//
// 🔴 THE SAME CLASS OF DEFECT AS ON THE POLICY RESOURCE, THROUGH A DIFFERENT
// DOOR. `effective_mode` here is Computed-only, and Terraform's proposed new
// state keeps a Computed-only attribute's PRIOR value across an update -- so
// with no ModifyPlan at all the plan silently promises the OLD policy's mode
// while the apply re-reads the NEW one.
//
// `policy_id` is the only attribute on this resource that can be changed
// without forcing a replacement, and re-pointing an attachment at another
// policy is exactly what changes the mode in force. So the promise is wrong
// precisely when it is made, and Terraform ends the run with
//
//	Provider produced inconsistent result after apply: .effective_mode:
//	was cty.StringVal("detect"), but now cty.StringVal("block")
//
// The inherit-driven variant the policy resource has to handle -- the GATEWAY
// policy flipping under an unchanged overlay -- is NOT reachable here: an
// unchanged policy_id changes nothing, so Terraform plans no apply and no
// promise is made. That is why the second case must keep the refreshed value
// rather than going unknown on inherit alone.
func TestTheAttachmentPlanIsHonestAboutTheEffectiveMode(t *testing.T) {
	// proposedNewState is what Terraform core hands the provider for an update
	// before any ModifyPlan runs: a Computed-only attribute keeps its PRIOR
	// STATE value. That default is the hazard -- it is a promise about a value
	// the server recomputes on every attach.
	proposedNewState := func(plan, state AttachmentModel) AttachmentModel {
		plan.ID = state.ID
		plan.Scope = state.Scope
		plan.EffectiveMode = state.EffectiveMode
		return plan
	}
	planned := func(t *testing.T, plan, state AttachmentModel) AttachmentModel {
		t.Helper()
		out := proposedNewState(plan, state)
		mp, ok := NewResource().(resource.ResourceWithModifyPlan)
		if !ok {
			return out
		}
		resp := resource.ModifyPlanResponse{Plan: planOf(t, out)}
		mp.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Plan: planOf(t, out), State: stateOf(t, state),
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("modify plan: %v", resp.Diagnostics.Errors())
		}
		resp.Plan.Get(context.Background(), &out)
		return out
	}
	// A settled attachment on a listener, carrying an overlay policy that the
	// gateway resolved to "detect" when the refresh ran.
	settled := func() AttachmentModel {
		m := model("wp-ov")
		m.ListenerID = types.StringValue("l-1")
		m.ID = types.StringValue("agw-1/l-1")
		m.Scope = types.StringValue(scopeOverlay)
		m.EffectiveMode = types.StringValue("detect")
		return m
	}

	t.Run("re-pointing the attachment at a policy in another mode", func(t *testing.T) {
		state := settled()
		plan := state
		plan.PolicyID = types.StringValue("wp-ov2") // the only non-replacing edit there is

		out := planned(t, plan, state)

		// The apply: the newly named policy inherits, and the gateway resolves
		// it to "block".
		next := overlayPolicy()
		next.ID = "wp-ov2"
		c, _ := api(t, next, next.ID)
		ar := &attachmentResource{client: c}
		resp := resource.UpdateResponse{State: stateOf(t, state)}
		ar.Update(context.Background(), resource.UpdateRequest{
			Plan: planOf(t, plan), State: stateOf(t, state),
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("update: %v", resp.Diagnostics.Errors())
		}
		var applied AttachmentModel
		resp.State.Get(context.Background(), &applied)
		if applied.EffectiveMode.ValueString() != "block" {
			t.Fatalf("the apply produced effective_mode = %v, want block -- without that this "+
				"test cannot see the inconsistency it exists for", applied.EffectiveMode)
		}

		if !out.EffectiveMode.IsUnknown() && !out.EffectiveMode.Equal(applied.EffectiveMode) {
			t.Fatalf("the plan promised effective_mode = %v and the apply produced %v, so "+
				"Terraform ends the run with \"Provider produced inconsistent result after "+
				"apply: .effective_mode: was cty.StringVal(%q), but now cty.StringVal(%q)\". "+
				"A different policy is a different mode, so the plan cannot carry the old "+
				"one's forward",
				out.EffectiveMode, applied.EffectiveMode,
				out.EffectiveMode.ValueString(), applied.EffectiveMode.ValueString())
		}
	})

	// 🔴 A GUARD AGAINST THE OVER-BROAD FIX, NOT EVIDENCE ABOUT THE ONE ABOVE:
	// this case passes with no ModifyPlan at all, and it fails only against one
	// that marks effective_mode unknown unconditionally. That version reads
	// reasonable and costs a perpetual diff: a plan that changes nothing would
	// report an update forever, and every apply would re-PUT an attachment
	// nobody asked to move.
	t.Run("an attachment with nothing to apply keeps the refreshed value", func(t *testing.T) {
		state := settled()
		out := planned(t, state, state)
		if out.EffectiveMode.IsUnknown() {
			t.Fatalf("effective_mode is unknown on an attachment with nothing to change, so "+
				"every plan reports an update; want the refreshed %v", state.EffectiveMode)
		}
		if !out.EffectiveMode.Equal(state.EffectiveMode) {
			t.Errorf("effective_mode = %v, want the refreshed %v", out.EffectiveMode, state.EffectiveMode)
		}
	})
}
