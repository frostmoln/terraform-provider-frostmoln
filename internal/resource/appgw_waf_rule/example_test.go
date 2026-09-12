package appgw_waf_rule

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// examplePath is the file tfplugindocs INLINES VERBATIM into the resource page,
// and from there into the docs-portal mirror. It is not decoration: it is the
// only complete builder rule a reader of either site ever sees, so a rule that
// would be refused is a rule they will copy and be refused for.
const examplePath = "../../../examples/resources/frostmoln_appgw_waf_rule/resource.tf"

func readExample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(examplePath))
	if err != nil {
		t.Fatalf("read %s: %v", examplePath, err)
	}
	s := string(b)
	// Guard against reading something that is not the example, which would make
	// every absence-check below pass for the wrong reason.
	if !strings.Contains(s, `resource "frostmoln_appgw_waf_rule"`) {
		t.Fatalf("%s does not contain a frostmoln_appgw_waf_rule resource", examplePath)
	}
	return s
}

var operatorRE = regexp.MustCompile(`operator\s*=\s*"([^"]+)"`)

// 🔴 EVERY BUILDER OPERATOR IS `@`-PREFIXED, AND A BARE ONE IS A 400.
//
// appgw matches `condition.operator` against its allow-list EXACTLY — there is
// no normalisation step that adds the `@`, and the published enum in
// appgw-api.yaml carries the prefix on all thirteen. Three conditions in this
// example shipped WITHOUT it (`rx`, `beginsWith`, `ipMatch`), so all three
// documented rules — the two this file already had, on the resource page and on
// its docs-portal mirror — were refused on the rule PUT by the only server that
// accepts them.
//
// The same mistake has been made and measured elsewhere: the acceptance suite's
// canary rule once sent a bare `contains` and 400'd, taking eleven tests with
// it. Pinned here because an example is the one place a mistake is copied
// verbatim by people who cannot see the allow-list.
func TestExampleOperatorsAreThePrefixedOnesTheServerAccepts(t *testing.T) {
	// appgw's allowedBuilderOperators, in the spelling the API publishes.
	permitted := map[string]bool{
		"@rx": true, "@streq": true, "@contains": true, "@beginsWith": true,
		"@endsWith": true, "@within": true, "@eq": true, "@gt": true, "@lt": true,
		"@ge": true, "@le": true, "@ipMatch": true, "@validateByteRange": true,
		"@pm": true,
	}

	matches := operatorRE.FindAllStringSubmatch(readExample(t), -1)
	if len(matches) == 0 {
		t.Fatal("the example declares no conditions at all, so this test proves nothing")
	}
	for _, m := range matches {
		op := m[1]
		if !strings.HasPrefix(op, "@") {
			t.Errorf("operator %q is not @-prefixed; appgw matches the allow-list exactly and answers 400", op)
			continue
		}
		if !permitted[op] {
			t.Errorf("operator %q is not one appgw permits on a builder rule", op)
		}
	}
}

// The example is where a reader meets the ACTIONS, and `allow` is the one whose
// plain English is wider than what it does. Before this change the file
// demonstrated `deny` three times and nothing else, so `log` and `allow` were
// reachable only by knowing they exist.
func TestExampleDemonstratesEveryBuilderAction(t *testing.T) {
	s := readExample(t)
	for _, want := range []string{`type    = "deny"`, `type    = "log"`, `type    = "allow"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the example never demonstrates %s", want)
		}
	}
}

// THE TWO FACTS A CUSTOMER GETS WRONG, in the comment beside the rule they will
// copy. A multi-condition allow is the shape this API invites first, and a
// partial-match defect exempted the path for the whole internet on exactly that
// shape; and `allow` exempts the anomaly-score decision and nothing else.
func TestTheAllowExampleStatesWhatItDoesAndDoesNotExempt(t *testing.T) {
	s := readExample(t)

	// It must be the MULTI-condition shape, or the first fact has nothing to be
	// true of: a one-condition allow cannot illustrate "both must match".
	allow := s[strings.Index(s, `"allow_uptime_checker"`):]
	if n := strings.Count(allow[:strings.Index(allow, "action = {")], "variable ="); n < 2 {
		t.Errorf("the allow example has %d conditions; it must show the ANDed shape customers reach for first", n)
	}

	for _, want := range []string{
		"EVERY CONDITION MUST MATCH",
		"allowed_methods",
		"allowed_request_content_types",
		"could not parse",
		"not scored",
		"phase = 2 IS REQUIRED",
		"refused rather than moved",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the allow example does not state %q", want)
		}
	}

	// And never the blanket-exemption reading. Two of the three ways the rule
	// language says "allow" DO skip the rest of the request inspection; the
	// builder emits neither, so describing this one in their words is false.
	for _, forbidden := range []string{"bypass", "skips inspection", "not inspected"} {
		if strings.Contains(strings.ToLower(s), forbidden) {
			t.Errorf("the example says %q, which describes an exemption this action does not grant", forbidden)
		}
	}
}

// builderJSONDescription reads the attribute's description off the LIVE schema
// rather than off the source text, so it asserts what tfplugindocs renders.
func builderJSONDescription(t *testing.T) string {
	t.Helper()
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	attr, ok := sr.Schema.Attributes["builder_json"]
	if !ok {
		t.Fatal("the schema has no builder_json attribute")
	}
	return attr.GetDescription()
}

// The schema description is the other half of the page — the part a reader
// reaches from the attribute list rather than from the example — and nothing
// else compares the two.
func TestBuilderJSONDescriptionCarriesTheAllowRules(t *testing.T) {
	desc := builderJSONDescription(t)
	if desc == "" {
		t.Fatal("builder_json has no description")
	}
	for _, want := range []string{
		"`deny`", "`log`", "`allow`",
		"Conditions are ANDed",
		"Every condition must match",
		"`allowed_methods`",
		"`allowed_request_content_types`",
		"could not parse",
		"not scored",
		"`phase = 2`",
		"refused rather than moved",
		`not available to ` + "`kind = \"raw\"`",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("the builder_json description does not carry %q", want)
		}
	}
}
