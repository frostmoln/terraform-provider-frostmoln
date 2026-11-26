package dns_record

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Vectors copied from network's TestNormalizeDNSRecordValues; keep in lockstep.
func TestStoredValue(t *testing.T) {
	cases := []struct{ typ, in, want string }{
		{"CAA", `0 issue "letsencrypt.org"`, `0 issue letsencrypt.org`},
		{"CAA", `0   issuewild  "ca.example; account=1" `, `0 issuewild ca.example; account=1`},
		{"CAA", `128 iodef "mailto:sec@example.com"`, `128 iodef mailto:sec@example.com`},
		{"CAA", `0 issue letsencrypt.org`, `0 issue letsencrypt.org`},
		{"CAA", `0 issue ""`, `0 issue ""`},
		{"CAA", `0 issue "a"b"`, `0 issue "a"b"`},
		{"CAA", `0 issue`, `0 issue`},
		{"TXT", `"quoted"`, `"quoted"`},
	}
	for _, c := range cases {
		if got := storedValue(c.typ, c.in); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.typ, c.in, got, c.want)
		}
	}
}

// A quoted CAA in configuration is read back bare; the configured spelling is
// kept so apply is consistent and plan shows no diff. A real change is not hidden.
func TestFromAPI_KeepsQuotedCAASpelling(t *testing.T) {
	ctx := context.Background()
	set := func(v ...string) types.Set {
		s, _ := types.SetValueFrom(ctx, types.StringType, v)
		return s
	}
	for _, tc := range []struct {
		name   string
		prior  types.Set
		stored []string
		want   types.Set
	}{
		{"quoted kept", set(`0 issue "letsencrypt.org"`), []string{"0 issue letsencrypt.org"}, set(`0 issue "letsencrypt.org"`)},
		{"drift shown", set(`0 issue "letsencrypt.org"`), []string{"0 issue pki.goog"}, set("0 issue pki.goog")},
		{"import takes API", types.SetNull(types.StringType), []string{"0 issue letsencrypt.org"}, set("0 issue letsencrypt.org")},
		{"two spellings of one value do not hide drift", set(`0 issue "x.example"`, "0 issue x.example"), []string{"0 issue x.example", "0 issue y.example"}, set("0 issue x.example", "0 issue y.example")},
		{"removed value shown", set(`0 issue "a.example"`, "0 issue b.example"), []string{"0 issue a.example"}, set("0 issue a.example")},
		{"extra stored value shown", set(`0 issue "a.example"`), []string{"0 issue a.example", "0 issue b.example"}, set("0 issue a.example", "0 issue b.example")},
	} {
		m := &DNSRecordModel{Records: tc.prior, Tags: types.MapNull(types.StringType)}
		var diags diag.Diagnostics
		m.fromAPI(ctx, &apiDNSRecord{Type: "CAA", Records: tc.stored}, &diags)
		if diags.HasError() {
			t.Fatalf("%s: %v", tc.name, diags)
		}
		if !m.Records.Equal(tc.want) {
			t.Errorf("%s: Records = %v, want %v", tc.name, m.Records, tc.want)
		}
	}
}
