package appgw_waf_policy_attachment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// writeNotAttached answers a conditional detach the way appgw does when the
// policy it names is not the one attached: 409, the FLAT envelope every appgw
// refusal uses, and details {expectedPolicyId, attachedPolicyId} with the
// latter null when nothing is attached.
func writeNotAttached(w http.ResponseWriter, expected, attached string) {
	details := map[string]any{"expectedPolicyId": expected, "attachedPolicyId": nil}
	if attached != "" {
		details["attachedPolicyId"] = attached
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    "WAF_POLICY_NOT_ATTACHED",
		"message": "the policy named is not the one attached here now, so nothing was detached",
		"details": details,
	})
}

// detachLevel is one attachment point, in the state a destroy starts from.
type detachLevel struct {
	name   string
	policy apiPolicy
	state  AttachmentModel
	path   string
}

func detachLevels() []detachLevel {
	gw := model("wp-gw")
	gw.ID = types.StringValue("agw-1")

	listener := model("wp-ov")
	listener.ListenerID = types.StringValue("l-1")
	listener.ID = types.StringValue("agw-1/l-1")

	route := model("wp-ov")
	route.ListenerID = types.StringValue("l-1")
	route.RouteID = types.StringValue("r-7")
	route.ID = types.StringValue("agw-1/l-1/r-7")

	return []detachLevel{
		{name: "gateway", policy: gatewayPolicy(), state: gw, path: base + "/waf-policy"},
		{name: "listener", policy: overlayPolicy(), state: listener, path: base + "/listeners/l-1/waf-policy"},
		{name: "route", policy: overlayPolicy(), state: route, path: base + "/listeners/l-1/routes/r-7/waf-policy"},
	}
}

func destroy(t *testing.T, ar *attachmentResource, state AttachmentModel) resource.DeleteResponse {
	t.Helper()
	resp := resource.DeleteResponse{State: stateOf(t, state)}
	ar.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, state)}, &resp)
	return resp
}

// TestDestroyNamesThePolicyInStateAsTheCondition.
//
// 🔴 A DETACH NAMES NO POLICY. Without `?policyId=` the API clears whatever is
// attached, so a destroy acting on a plan that has gone stale -- someone
// re-pointed the gateway at another policy between plan and apply -- turns off
// inspection by a policy this configuration never managed and the plan never
// showed. The id in STATE is the one the plan displayed as being detached, so
// that is the one the destroy has to name.
func TestDestroyNamesThePolicyInStateAsTheCondition(t *testing.T) {
	for _, lv := range detachLevels() {
		t.Run(lv.name, func(t *testing.T) {
			c, seen := api(t, lv.policy, lv.state.PolicyID.ValueString())
			resp := destroy(t, &attachmentResource{client: c}, lv.state)
			if resp.Diagnostics.HasError() {
				t.Fatalf("delete: %v", resp.Diagnostics.Errors())
			}
			if len(*seen) != 1 {
				t.Fatalf("expected exactly one write, got %+v", *seen)
			}
			got := (*seen)[0]
			if got.method != http.MethodDelete || got.path != lv.path {
				t.Fatalf("detach went to %s %s, want DELETE %s", got.method, got.path, lv.path)
			}
			q, err := url.ParseQuery(got.query)
			if err != nil {
				t.Fatalf("query %q: %v", got.query, err)
			}
			want := lv.state.PolicyID.ValueString()
			if ids := q["policyId"]; len(ids) != 1 || ids[0] != want {
				t.Fatalf("detach query = %q, want policyId=%s exactly once -- an unconditional "+
					"detach clears whatever is attached, including a policy this "+
					"configuration never managed", got.query, want)
			}
			if len(q) != 1 {
				t.Errorf("detach query carries more than the expected policy: %q", got.query)
			}
		})
	}
}

// TestDestroyOfAnAttachmentChangedOutOfBandDetachesNothing.
//
// The 409 WAF_POLICY_NOT_ATTACHED says the attachment this resource managed no
// longer exists: another policy is attached there now, or nothing is. Either
// way there is nothing left for this destroy to remove, so it SUCCEEDS -- a
// destroy that failed here would be unrecoverable without `terraform state rm`,
// since every retry meets the same state.
//
// 🔴 AND IT MUST NOT FALL BACK TO THE UNCONDITIONAL DETACH. "Retry without the
// condition" is the obvious way to make the error go away, and it is exactly
// the detach of a policy somebody else attached that the condition exists to
// prevent. Exactly one request, then.
//
// The warning names what IS attached, because that is the fact the
// practitioner's state got wrong -- and at the gateway it must not carry the
// "inheriting overlays no longer block" alarm, which asserts a detach that did
// not happen.
func TestDestroyOfAnAttachmentChangedOutOfBandDetachesNothing(t *testing.T) {
	for _, lv := range detachLevels() {
		for _, now := range []struct {
			name     string
			attached string
			say      string
		}{
			{name: "another policy attached", attached: "wp-other", say: "wp-other"},
			{name: "nothing attached", attached: "", say: "no WAF policy"},
		} {
			t.Run(lv.name+"/"+now.name, func(t *testing.T) {
				c, seen := api(t, lv.policy, now.attached)
				resp := destroy(t, &attachmentResource{client: c}, lv.state)

				if resp.Diagnostics.HasError() {
					t.Fatalf("a destroy whose attachment is already gone failed, and every retry "+
						"meets the same state: %v", resp.Diagnostics.Errors())
				}
				if len(*seen) != 1 {
					t.Fatalf("expected exactly the one conditional detach, got %+v -- a second "+
						"request is the unconditional detach of a policy this configuration "+
						"never managed", *seen)
				}
				if !strings.Contains((*seen)[0].query, "policyId=") {
					t.Fatalf("the detach was not conditional: %+v", (*seen)[0])
				}
				if resp.Diagnostics.WarningsCount() != 1 {
					t.Fatalf("want exactly one warning, got %v", resp.Diagnostics.Warnings())
				}
				warning := joinWarnings(resp.Diagnostics)
				if !strings.Contains(warning, now.say) {
					t.Errorf("the warning does not say what is attached now (%q): %s", now.say, warning)
				}
				if !strings.Contains(warning, lv.state.PolicyID.ValueString()) {
					t.Errorf("the warning does not name the policy this resource managed: %s", warning)
				}
				if strings.Contains(warning, "has been detached") {
					t.Errorf("the warning claims a detach that did not happen: %s", warning)
				}
			})
		}
	}
}

// TestDestroyStillFailsOnEveryOtherRefusal. Only the one code that means "the
// attachment you managed is gone" is a success; a 409 that means anything
// else, a 5xx, or the same code at the wrong status is a detach that did not
// happen for a reason the practitioner has to see. Guards the over-broad fix
// (IsConflict) -- this passes against the unconditional detach too.
func TestDestroyStillFailsOnEveryOtherRefusal(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"another 409", http.StatusConflict, `{"code":"GATEWAY_DELETING","message":"the gateway is being deleted"}`},
		{"a nested 409", http.StatusConflict, `{"error":{"code":"CONFLICT","message":"conflict"}}`},
		{"the code at 400", http.StatusBadRequest, `{"code":"WAF_POLICY_NOT_ATTACHED","message":"x"}`},
		{"an invalid policy id", http.StatusBadRequest, `{"code":"INVALID_REQUEST","message":"policyId must be a uuid"}`},
		{"a 500", http.StatusInternalServerError, `{"code":"INTERNAL","message":"an internal error occurred"}`},
	}
	for _, lv := range detachLevels() {
		for _, tc := range cases {
			t.Run(lv.name+"/"+tc.name, func(t *testing.T) {
				var deletes int
				c := serve(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodDelete {
						deletes++
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				})
				resp := destroy(t, &attachmentResource{client: c}, lv.state)
				if !resp.Diagnostics.HasError() {
					t.Fatalf("a detach refused with %d %s was reported as done", tc.status, tc.body)
				}
				if deletes != 1 {
					t.Fatalf("expected exactly one detach, got %d", deletes)
				}
			})
		}
	}
}

// TestDestroyWithNoPolicyInStateDetachesUnconditionally. An empty `policyId=`
// is a 400 INVALID_REQUEST, which would make the destroy fail on every retry;
// with no id to name, the only detach there is to send is the plain one.
func TestDestroyWithNoPolicyInStateDetachesUnconditionally(t *testing.T) {
	c, seen := api(t, gatewayPolicy(), "wp-gw")
	state := model("")
	state.PolicyID = types.StringNull()
	state.ID = types.StringValue("agw-1")

	resp := destroy(t, &attachmentResource{client: c}, state)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics.Errors())
	}
	if len(*seen) != 1 || (*seen)[0].query != "" {
		t.Fatalf("want one unconditional DELETE, got %+v", *seen)
	}
}
