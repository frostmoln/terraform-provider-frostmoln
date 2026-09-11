package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheWordEngineIsRetiredOnPublishedSurfaces scans what a customer actually reads: the
// generated Registry pages under docs/ and the example HCL under examples/.
//
// The operator's rule, restated more than once: a managed offer's implementations are never
// called an "engine". PostgreSQL and MySQL share neither a wire protocol, a dialect, a parameter
// vocabulary nor a backup format; Apache and Nginx take different configuration; Redis and Valkey
// have diverged. The word tells a customer they are interchangeable.
//
// This scans docs/ rather than the Go sources on purpose. docs/ is GENERATED from the schema
// descriptions, so a description that still says "engine" reaches the Terraform Registry whether
// or not anyone remembers to look — and a description can be wrong in a way the compiler cannot
// see (one of them claimed the provider sends an `typeConfig` object after it had started
// sending `typeConfig`).
func TestTheWordEngineIsRetiredOnPublishedSurfaces(t *testing.T) {
	// allowed maps a published page to why "engine" legitimately survives on it. Every entry is
	// the Application Gateway's WAF INSPECTION engine — a component, never a choice the customer
	// makes between interchangeable implementations.
	allowed := map[string]string{
		"docs/resources/appgw_waf_policy.md":                        "the WAF inspection engine",
		"docs/resources/appgw_waf_rule.md":                          "the WAF inspection engine",
		"examples/resources/frostmoln_appgw_waf_policy/resource.tf": "the WAF inspection engine",
		"examples/resources/frostmoln_appgw_waf_rule/resource.tf":   "the WAF inspection engine",
	}

	roots := []string{"../../docs", "../../examples"}
	scanned, flagged := 0, 0

	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".md" && ext != ".tf" {
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, "../../"))
			if _, ok := allowed[rel]; ok {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for i, line := range strings.Split(string(b), "\n") {
				if !strings.Contains(strings.ToLower(line), "engine") {
					continue
				}
				flagged++
				t.Errorf(`%s:%d publishes "engine": %s

This page reaches the Terraform Registry. A managed offer's implementations are TYPES. If this is
a genuine engine that is not a customer's choice between implementations, add the page to the
allowlist in this test with the reason.`, rel, i+1, strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if scanned == 0 {
		t.Fatal("scanned no published pages — the guard is inert, not passing")
	}
	t.Logf("scanned %d published pages, %d allowlisted, %d violations", scanned, len(allowed), flagged)

	// 🔴 SCANNING docs/ ALONE IS NOT ENOUGH, and a mutation proved it: changing a schema
	// Description in Go left this test green, because docs/ only changes when gen-docs.sh runs.
	// CI's docs-drift gate would have caught the staleness eventually, but a guard that depends on
	// another gate firing first is not a guard.
	//
	// It scans EVERY line, not just descriptions — the wire tags matter as much. The provider's
	// own tests encode fixtures from the provider's own structs, so flipping a `json:"type"` tag
	// back to `json:"engine"` on both sides leaves them green; nothing in the unit suite pins the
	// bytes. This does.
	srcAllowed := map[string]string{
		"internal/resource/messaging_instance/resource.go":           "the engine -> type STATE UPGRADER, which must name the pre-rename attribute to read old state",
		"internal/resource/messaging_instance/state_upgrade_test.go": "the test for that upgrader — it constructs v0 state, which carries `engine`",
		"internal/resource/instance/resource_test.go":                "frostmoln_engine is an OpenStack instance-metadata key the SERVER stamps; the provider only mirrors it in a fixture and never reads it, so retiring it is a provisioning change, not a provider one",
	}
	descScanned, descFlagged := 0, 0
	err := filepath.Walk("../../internal", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "appgw") || strings.Contains(path, "waf") {
			return nil // the WAF inspection engine
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "../../"))
		if _, ok := srcAllowed[rel]; ok {
			return nil
		}
		if strings.HasSuffix(path, "no_engine_vocabulary_test.go") {
			return nil // this file states the rule, so it contains the word
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		descScanned++
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(strings.ToLower(line), "engine") {
				continue
			}
			descFlagged++
			t.Errorf(`%s:%d contains "engine": %s

A managed offer's implementations are TYPES. This covers wire tags, diagnostics and descriptions:
a `+"`json:\"engine\"`"+` tag here is invisible to the unit tests, which encode fixtures from these
very structs. If this is a genuine engine that is not a customer's choice between implementations,
add the file to srcAllowed with the reason.`, rel, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}
	if descScanned == 0 {
		t.Fatal("scanned no Go sources — that half of the guard is inert")
	}
	t.Logf("scanned %d Go sources, %d violations", descScanned, descFlagged)
}
