package appgw_waf_policy_publication

import (
	"context"
	"encoding/json"
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

const pubBase = "/v1/tenants/t-1/application-gateways/agw-1/waf-policies/wp-1"

func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m PublicationModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m PublicationModel) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: schemaOf(t).Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return s
}

func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: schemaOf(t).Schema}
}

func revisions(t *testing.T) types.Map {
	t.Helper()
	m, d := types.MapValueFrom(context.Background(), types.Int64Type,
		map[string]int64{"block-admin": 4})
	if d.HasError() {
		t.Fatalf("revisions: %v", d.Errors())
	}
	return m
}

func pubModel(t *testing.T, maxBlocked types.Int64) PublicationModel {
	return PublicationModel{
		GatewayID:       types.StringValue("agw-1"),
		PolicyID:        types.StringValue("wp-1"),
		RuleRevisions:   revisions(t),
		MaxNewlyBlocked: maxBlocked,
	}
}

func shrinkWaits(t *testing.T) {
	t.Helper()
	oi, ot := dryRunPollInterval, dryRunTimeout
	dryRunPollInterval, dryRunTimeout = 5*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { dryRunPollInterval, dryRunTimeout = oi, ot })
}

func serve(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

// TestPublishRunsTheDryRunFirst is the ordering the whole resource exists for:
// a publish that has not been replayed is refused by the server, so the client
// must not attempt one.
func TestPublishRunsTheDryRunFirst(t *testing.T) {
	var order []string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		order = append(order, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == pubBase+"/dry-runs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{
				ID: "d-1", ContentHash: "abc", Status: "completed",
				RequestsSampled: 500, NewlyBlocked: 0})
		case r.URL.Path == pubBase+"/publish":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiVersion{Version: 4, State: "frozen", ContentHash: "abc"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	r := &publicationResource{client: c}

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if len(order) != 2 || !strings.HasSuffix(order[0], "/dry-runs") || !strings.HasSuffix(order[1], "/publish") {
		t.Fatalf("the dry-run must run BEFORE the publish, got %v", order)
	}

	var out PublicationModel
	resp.State.Get(context.Background(), &out)
	if out.Version.ValueInt64() != 4 || out.ID.ValueString() != "wp-1/4" {
		t.Fatalf("publication state wrong: %+v", out)
	}
	if out.DryRunRequestsSampled.ValueInt64() != 500 {
		t.Errorf("requests_sampled = %v, want 500", out.DryRunRequestsSampled)
	}
}

// TestPublishIsRefusedOverTheBudget.
//
// 🔴 THE BUDGET IS THE SAFETY VALVE, AND NOTHING MUST BE PUBLISHED WHEN IT IS
// EXCEEDED. The error also has to name the traffic that would break: a count
// alone tells an operator nothing they can act on.
func TestPublishIsRefusedOverTheBudget(t *testing.T) {
	var published bool
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == pubBase+"/dry-runs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{
				ID: "d-1", Status: "completed", RequestsSampled: 500, NewlyBlocked: 42,
				Sample: []apiDryRunSample{{
					Method: "POST", Host: "api.example.com", Path: "/v1/search",
					MatchedRuleKey: "block-sqli", OccurrenceCount: 42}},
			})
		case r.URL.Path == pubBase+"/publish":
			published = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiVersion{Version: 4})
		}
	})
	r := &publicationResource{client: c}

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a dry-run over the budget must refuse the publication")
	}
	if published {
		t.Fatal("the ruleset was PUBLISHED despite exceeding max_newly_blocked")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "/v1/search") || !strings.Contains(detail, "block-sqli") {
		t.Fatalf("the refusal must name the traffic that would break:\n%s", detail)
	}
}

// TestPublishWarnsButProceedsWithNoBudget. Omitting max_newly_blocked means
// "publish whatever the dry-run reports" — but it must still be reported.
func TestPublishWarnsButProceedsWithNoBudget(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/dry-runs") {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{
				ID: "d-1", Status: "completed", RequestsSampled: 10, NewlyBlocked: 2,
				Sample: []apiDryRunSample{{Method: "GET", Host: "h", Path: "/p", OccurrenceCount: 2}}})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(apiVersion{Version: 5, ContentHash: "z"})
	})
	r := &publicationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Null()))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("with no budget the publication must proceed: %v", resp.Diagnostics.Errors())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("newly-blocked traffic must be reported even when it is within budget")
	}
}

// TestUnchangedDraftPublishesNothingAndStillNamesWhatIsEnforced.
//
// A 204 is a successful no-op — it is what makes a repeated apply idempotent
// rather than cutting a version nobody asked for. State must still name the
// version being enforced, or the next plan has nothing to compare.
func TestUnchangedDraftPublishesNothingAndStillNamesWhatIsEnforced(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/dry-runs") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{
				ID: "d-1", Status: "completed", RequestsSampled: 120, ContentHash: "same"})
		case strings.HasSuffix(r.URL.Path, "/publish"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/versions/active"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": apiVersion{Version: 3, State: "frozen", ContentHash: "same"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	r := &publicationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a 204 is a success, not a failure: %v", resp.Diagnostics.Errors())
	}
	var out PublicationModel
	resp.State.Get(context.Background(), &out)
	if out.Version.ValueInt64() != 3 {
		t.Fatalf("version = %v; an unchanged publish must still name the enforced version", out.Version)
	}
}

// TestAPendingDryRunNamesTheRealCause.
//
// 🔴 THE REPLAY RUNS ON THE APPLIANCE. A dry-run stays pending when the
// appliance is not running the inspection engine, and "did not complete in
// 5m0s" sends an operator to debug Terraform instead of the gateway.
func TestAPendingDryRunNamesTheRealCause(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{ID: "d-1", Status: "pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(apiDryRunListResponse{
			DryRuns: []apiDryRun{{ID: "d-1", Status: "pending"}}})
	})
	r := &publicationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a dry-run that never completes must fail the apply")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"appliance", "inspection engine", "config_status"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the error must point at the gateway, not at Terraform; missing %q:\n%s",
				want, detail)
		}
	}
}

// TestAFailedDryRunSurfacesItsOwnError rather than a generic timeout.
func TestAFailedDryRunSurfacesItsOwnError(t *testing.T) {
	shrinkWaits(t)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{ID: "d-1", Status: "pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(apiDryRunListResponse{DryRuns: []apiDryRun{
			{ID: "d-1", Status: "failed", Error: "the ruleset exceeded its evaluation budget"}}})
	})
	r := &publicationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed dry-run must fail the apply")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "evaluation budget") {
		t.Errorf("the dry-run's own words must survive:\n%s", resp.Diagnostics.Errors()[0].Detail())
	}
}

// TestDeleteSaysTheVersionIsStillEnforced. Silently succeeding would imply the
// ruleset stopped being enforced; there is no unpublish.
func TestDeleteSaysTheVersionIsStillEnforced(t *testing.T) {
	var resp resource.DeleteResponse
	(&publicationResource{}).Delete(context.Background(), resource.DeleteRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete must not error, or terraform destroy wedges: %v", resp.Diagnostics.Errors())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("destroy must say the published version is still enforced")
	}
}

// TestReadTracksWhatIsEnforced. A publication is an ACT, not a stored object;
// refreshing means asking what is enforced now.
func TestReadTracksWhatIsEnforced(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/versions/active") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": apiVersion{Version: 9, ContentHash: "newer"}})
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	})
	r := &publicationResource{client: c}
	prior := pubModel(t, types.Int64Value(0))
	prior.Version = types.Int64Value(4)
	resp := resource.ReadResponse{State: stateOf(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateOf(t, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out PublicationModel
	resp.State.Get(context.Background(), &out)
	if out.Version.ValueInt64() != 9 {
		t.Fatalf("version = %v; read must report what is enforced now", out.Version)
	}
}

// TestAZeroSampleDryRunCannotSatisfyTheBudget.
//
// 🔴 A GATEWAY WITH NO TRAFFIC PRODUCES A COMPLETED DRY-RUN THAT MEASURED
// NOTHING: 0 sampled, 0 newly blocked. `0 > 0` is false, so max_newly_blocked = 0
// would have waved through a ruleset that refuses every request the site
// actually serves. Passing a budget against an empty sample means nothing, and
// saying so is the only honest answer.
func TestAZeroSampleDryRunCannotSatisfyTheBudget(t *testing.T) {
	var published bool
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/dry-runs") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{
				ID: "d-1", Status: "completed", RequestsSampled: 0, NewlyBlocked: 0})
			return
		}
		published = true
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(apiVersion{Version: 4})
	})
	r := &publicationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a dry-run that sampled nothing must not satisfy the budget")
	}
	if published {
		t.Fatal("the ruleset was published on the strength of a measurement of nothing")
	}

	// With NO budget the practitioner has asked for no gate, so an empty sample
	// is not an error — it is just a weak signal, already recorded in state.
	resp = resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Null()))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("with no budget an empty sample must not block the publish: %v",
			resp.Diagnostics.Errors())
	}
}

// TestPublishingADifferentRulesetThanWasReplayedIsRefused.
//
// The server's gate accepts ANY completed dry-run matching the draft at publish
// time, not specifically the one this apply started. So a concurrent editor can
// move the draft between the two calls, and the publish ships content that
// max_newly_blocked never measured. Recording one dry-run's numbers beside
// another ruleset's hash would be attesting to a measurement that does not
// describe what was published.
func TestPublishingADifferentRulesetThanWasReplayedIsRefused(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/dry-runs") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDryRun{
				ID: "d-1", Status: "completed", RequestsSampled: 500, ContentHash: "measured"})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		// Someone else moved the draft in between.
		_ = json.NewEncoder(w).Encode(apiVersion{Version: 7, ContentHash: "something-else"})
	})
	r := &publicationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, pubModel(t, types.Int64Value(0)))}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("publishing a ruleset the dry-run never measured must be refused")
	}
	// The version IS published; state must record it, or Terraform is unaware
	// of a change that happened.
	if resp.State.Raw.IsNull() {
		t.Fatal("the published version was dropped from state")
	}
	var out PublicationModel
	resp.State.Get(context.Background(), &out)
	if out.Version.ValueInt64() != 7 {
		t.Fatalf("version = %v, want the version that was actually published", out.Version)
	}
}

// TestReadRefusesToAdoptAVersionItDidNotPublish.
//
// version/content_hash/published_at are Computed with no config counterpart, so
// silently absorbing whatever is active would make the next plan report no
// changes — and the one resource whose purpose is to assert "this ruleset is
// enforced" would agree with a ruleset nobody in the configuration chose. An
// out-of-band rollback is the case that costs something: rollback is
// deliberately exempt from the dry-run gate.
func TestReadRefusesToAdoptAVersionItDidNotPublish(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": apiVersion{Version: 2, ContentHash: "rolled-back"}})
	})
	r := &publicationResource{client: c}
	prior := pubModel(t, types.Int64Value(0))
	prior.Version = types.Int64Value(4)
	prior.ContentHash = types.StringValue("what-we-published")

	resp := resource.ReadResponse{State: stateOf(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateOf(t, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("the resource adopted a version it did not publish; the next plan would report " +
			"no changes while the gateway enforces someone else's ruleset")
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("removing it from state must say why")
	}
}
