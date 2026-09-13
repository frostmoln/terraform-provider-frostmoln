package tagsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
)

// The request body always carries `value`: null for a key-only rule, "" for the
// empty-value rule — never omitted, and never "" collapsed into null.
func TestRuleRequestAlwaysCarriesValue(t *testing.T) {
	empty := ""
	for _, tc := range []struct {
		value *string
		want  string
	}{
		{nil, `{"key":"env","value":null,"color":"#d73a49"}`},
		{&empty, `{"key":"env","value":"","color":"#d73a49"}`},
	} {
		b, err := json.Marshal(RuleRequest{Key: "env", Value: tc.value, Color: "#d73a49"})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("marshal = %s, want %s", b, tc.want)
		}
	}
}

func TestRuleDecodeKeepsNullAndEmptyApart(t *testing.T) {
	var null, empty Rule
	if err := json.Unmarshal([]byte(`{"id":"a","key":"k","value":null,"color":"#000000"}`), &null); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"b","key":"k","value":"","color":"#000000"}`), &empty); err != nil {
		t.Fatal(err)
	}
	if null.Value != nil {
		t.Errorf("null value decoded as %q", *null.Value)
	}
	if empty.Value == nil || *empty.Value != "" {
		t.Errorf("empty value decoded as %v", empty.Value)
	}
}

func TestPathsEscapeTheirSegments(t *testing.T) {
	if got := OrgRulePath("a/b", "c?d"); got != "/v1/organizations/a%2Fb/tag-colors/c%3Fd" {
		t.Errorf("OrgRulePath = %q", got)
	}
}

func TestRuleExistsID(t *testing.T) {
	withID := &client.APIError{Code: CodeRuleExists, StatusCode: 409, Details: map[string]any{"ruleId": "r-1"}}
	if id, ok := RuleExistsID(fmt.Errorf("wrapped: %w", withID)); !ok || id != "r-1" {
		t.Errorf("RuleExistsID(with id) = %q, %v", id, ok)
	}
	noID := &client.APIError{Code: CodeRuleExists, StatusCode: 409}
	if id, ok := RuleExistsID(noID); !ok || id != "" {
		t.Errorf("RuleExistsID(backstop, no id) = %q, %v", id, ok)
	}
	limit := &client.APIError{Code: CodeRuleLimitReached, StatusCode: 409, Details: map[string]any{"max": float64(200)}}
	if _, ok := RuleExistsID(limit); ok {
		t.Error("the limit refusal is not an exists refusal")
	}
	if _, ok := RuleExistsID(errors.New("boom")); ok {
		t.Error("a non-API error is not an exists refusal")
	}
}

// Each check mirrors identity's ValidateTagColorRule at BOTH edges: refused
// where the server refuses, accepted just inside, so the plan-time check is
// never stricter than the platform.
func TestChecksMirrorTheServer(t *testing.T) {
	cases := []struct {
		name   string
		got    string
		refuse bool
	}{
		{"key empty", CheckKey(""), true},
		{"key 1", CheckKey("k"), false},
		{"key 255 runes", CheckKey(strings.Repeat("ä", 255)), false},
		{"key 256 runes", CheckKey(strings.Repeat("ä", 256)), true},
		{"key spaces and slashes", CheckKey("a b/c:d"), false},
		{"key tab", CheckKey("a\tb"), true},
		{"key zero-width joiner (Cf)", CheckKey("a\u200db"), true},
		{"key paragraph separator", CheckKey("a\u2029b"), true},
		{"key invalid utf-8", CheckKey("a\xffb"), true},
		{"key frostmoln_", CheckKey("frostmoln_x"), true},
		{"key frostmoln-", CheckKey("frostmoln-x"), true},
		{"key Frostmoln_ (case-sensitive)", CheckKey("Frostmoln_x"), false},
		{"key FROSTMOLN-", CheckKey("FROSTMOLN-x"), false},
		{"key frostmoln (no separator)", CheckKey("frostmolnx"), false},
		{"value empty", CheckValue(""), false},
		{"value 256 runes", CheckValue(strings.Repeat("ö", 256)), false},
		{"value 257 runes", CheckValue(strings.Repeat("ö", 257)), true},
		{"value quotes", CheckValue(`"'\`), false},
		{"value newline", CheckValue("a\nb"), true},
		{"color lower", CheckColor("#abcdef"), false},
		{"color upper", CheckColor("#ABCDEF"), false},
		{"color 3-digit", CheckColor("#abc"), true},
		{"color non-hex", CheckColor("#abcdeg"), true},
		{"color trailing newline", CheckColor("#abcdef\n"), true},
		{"org uuid", CheckOrganizationID("22222222-2222-4222-8222-222222222222"), false},
		// uuid.Parse accepts these spellings, but a non-canonical id would read
		// as a different organization to Terraform: refused, naming the form.
		{"org uuid braces", CheckOrganizationID("{22222222-2222-4222-8222-222222222222}"), true},
		{"org uuid upper case", CheckOrganizationID("ABCDEF00-2222-4222-8222-222222222222"), true},
		{"org uuid urn", CheckOrganizationID("urn:uuid:22222222-2222-4222-8222-222222222222"), true},
		{"org dot-dot", CheckOrganizationID(".."), true},
		{"org name", CheckOrganizationID("acme"), true},
		{"rule uuid", CheckRuleID("44444444-4444-4444-8444-444444444444"), false},
		{"rule empty", CheckRuleID(""), true},
	}
	for _, tc := range cases {
		if refused := tc.got != ""; refused != tc.refuse {
			t.Errorf("%s: refused = %v (%q), want %v", tc.name, refused, tc.got, tc.refuse)
		}
	}
}

func TestCheckOrganizationIDNamesTheCanonicalSpelling(t *testing.T) {
	got := CheckOrganizationID("{ABCDEF00-2222-4222-8222-222222222222}")
	if !strings.Contains(got, `"abcdef00-2222-4222-8222-222222222222"`) {
		t.Errorf("the refusal must name the canonical spelling, got %q", got)
	}
}

func TestResolveOrganizationIDIsCanonicalAndCachedPerClient(t *testing.T) {
	f := tagsettingstest.New(t)
	f.OrgID = "ABCDEF00-2222-4222-8222-222222222222" // a platform reporting upper case
	c := client.NewClient(f.Server.URL, "test-key")  // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Reset()
	for i := 0; i < 3; i++ {
		got, err := ResolveOrganizationID(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if got != "abcdef00-2222-4222-8222-222222222222" {
			t.Fatalf("ResolveOrganizationID = %q, want the canonical form", got)
		}
	}
	if n := len(f.Requests()); n != 1 {
		t.Errorf("the tenant route was read %d times for one client, want 1 (cached)", n)
	}
}

func TestResolveOrganizationIDDoesNotCacheAFailure(t *testing.T) {
	f := tagsettingstest.New(t)
	f.ReportOrganization = false
	c := client.NewClient(f.Server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveOrganizationID(context.Background(), c); !errors.Is(err, ErrOrganizationUnresolved) {
		t.Fatalf("err = %v, want ErrOrganizationUnresolved", err)
	}
	f.ReportOrganization = true
	if got, err := ResolveOrganizationID(context.Background(), c); err != nil || got != f.OrgID {
		t.Errorf("after the platform reports it: %q, %v", got, err)
	}
}
