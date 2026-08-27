package appgw_waf_policy_publication

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestFormatSampleNamesTheTrafficThatWouldBreak.
//
// The count alone ("42 requests would newly be blocked") tells an operator
// nothing they can act on. The shapes do: they are what turns "something
// breaks" into "the search endpoint breaks".
func TestFormatSampleNamesTheTrafficThatWouldBreak(t *testing.T) {
	out := formatSample([]apiDryRunSample{
		{Method: "GET", Host: "www.example.com", Path: "/", MatchedSecRule: 942100, OccurrenceCount: 3},
		{Method: "POST", Host: "api.example.com", Path: "/v1/search", MatchedRuleKey: "block-sqli", OccurrenceCount: 41},
	})

	// Most frequent first: the one that breaks 41 requests must not be below
	// the one that breaks 3.
	iSearch := strings.Index(out, "/v1/search")
	iRoot := strings.Index(out, "www.example.com")
	if iSearch < 0 || iRoot < 0 {
		t.Fatalf("both shapes must appear:\n%s", out)
	}
	if iSearch > iRoot {
		t.Errorf("the sample is not ordered by frequency:\n%s", out)
	}
	// A rule KEY is more use than a numeric id, so it wins where present.
	if !strings.Contains(out, "block-sqli") {
		t.Errorf("the matched rule key is missing:\n%s", out)
	}
	if !strings.Contains(out, "942100") {
		t.Errorf("the numeric SecRule id is missing where there is no key:\n%s", out)
	}
}

// TestFormatSampleWithNoSampleSaysSo. Printing nothing would read as "no
// requests are affected", which is the opposite of what an empty sample means.
func TestFormatSampleWithNoSampleSaysSo(t *testing.T) {
	out := formatSample(nil)
	if !strings.Contains(strings.ToLower(out), "no sample") {
		t.Fatalf("an empty sample must say so rather than print nothing: %q", out)
	}
}

// TestFormatSampleCapsTheList. A dry-run over a large corpus can return many
// shapes; an unbounded dump buries the ones that matter.
func TestFormatSampleCapsTheList(t *testing.T) {
	many := make([]apiDryRunSample, 25)
	for i := range many {
		many[i] = apiDryRunSample{Method: "GET", Host: "h", Path: "/p", OccurrenceCount: 25 - i}
	}
	out := formatSample(many)
	if !strings.Contains(out, "and 15 more shapes") {
		t.Fatalf("the list is not capped, or does not say how much it dropped:\n%s", out)
	}
}

// TestApplyVersionBuildsAStableID. The id must identify the publication, and a
// publication is (policy, version).
func TestApplyVersionBuildsAStableID(t *testing.T) {
	m := &PublicationModel{}
	m.PolicyID = stringValue("wp-1")
	m.applyVersion(&apiVersion{Version: 4, ContentHash: "abc", PublishedAt: "2026-08-01T00:00:00Z"})
	if got := m.ID.ValueString(); got != "wp-1/4" {
		t.Fatalf("id = %q, want wp-1/4", got)
	}
	if m.Version.ValueInt64() != 4 {
		t.Errorf("version = %v, want 4", m.Version)
	}
}

func stringValue(s string) types.String { return types.StringValue(s) }
