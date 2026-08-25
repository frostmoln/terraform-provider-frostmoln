// Package failclosed provides the shared assertion for the rule that a 404 which
// is a STATEMENT ABOUT ROUTING must never be read as "this resource is gone".
// The rule is stated in claude-config/conventions.md; the reasoning is in
// project-docs/product/TERRAFORM-404-FAIL-CLOSED-PLAN.md.
//
// It exists because the natural regression test for that contract has no
// discriminating power. Before client.IsNotFound required a flat envelope, the
// predicate was status-only, so BOTH envelope shapes returned true — which means
// every per-resource not-found fixture in this repo passes identically against
// the old code and the new. Rewriting those fixtures from nested to flat was
// correct (no service sends nested for a resource-not-found) but it is
// behaviour-neutral by construction: not one of them fails if someone widens
// IsNotFound back to reading the status alone.
//
// What actually pins the contract is feeding a resource the api-gateway's OWN
// unrouted-path body and asserting it does not destroy state. That is what this
// package does, so every backing-service family can assert it in three lines
// instead of nobody asserting it at all.
package failclosed

import (
	"encoding/json"
	"net/http"
	"testing"
)

// GatewayNotRouted writes the body api-gateway/internal/gateway/gateway.go emits
// when no upstream matches a path under /api. Nested, and deliberately so — it is
// what makes the discriminator work.
func GatewayNotRouted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "PATH_NOT_ROUTED",
			"message": "no route found for path",
		},
	})
}

// ServiceNotFound writes servicekit's flat envelope — a backing service's own
// verdict that the resource does not exist. This one SHOULD drop the resource.
func ServiceNotFound(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    "NOT_FOUND",
		"message": message,
	})
}

// AssertReadKeepsState fails when a routing 404 reaching Read removed the
// resource from state, or did so quietly. stateWasRemoved and hasError are read
// AFTER the caller has run its resource's Read against a GatewayNotRouted server.
func AssertReadKeepsState(t *testing.T, resourceName string, stateWasRemoved, hasError bool) {
	t.Helper()
	if stateWasRemoved {
		t.Errorf("%s: THE BUG — the resource was removed from state because the gateway "+
			"could not route the path. It still exists and is now unmanaged.", resourceName)
	}
	if !hasError {
		t.Errorf("%s: a routing failure must be a LOUD error, not a silent no-op", resourceName)
	}
}

// AssertDeleteDoesNotReportSuccess fails when a routing 404 reaching Delete was
// swallowed as a successful destroy.
func AssertDeleteDoesNotReportSuccess(t *testing.T, resourceName string, hasError bool) {
	t.Helper()
	if !hasError {
		t.Errorf("%s: THE BUG — `terraform destroy` reported success against a gateway "+
			"that never routed the delete. The resource is still live.", resourceName)
	}
}
