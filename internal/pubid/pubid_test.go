package pubid

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/vectors.json is a verbatim copy of servicekit pubid/testdata/vectors.json.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []struct{ Kind, UUID, Public string }
	if err := json.Unmarshal(raw, &vs); err != nil || len(vs) < 10 {
		t.Fatalf("vectors: %v (%d)", err, len(vs))
	}
	for _, v := range vs {
		if got, ok := Encode(v.Kind, strings.ToUpper(v.UUID)); !ok || got != v.Public {
			t.Errorf("Encode(%s, %s) = %q, %v; want %q", v.Kind, v.UUID, got, ok, v.Public)
		}
		if id, p, ok := Canonical(v.Public); !ok || id != v.UUID || p != v.Kind {
			t.Errorf("Canonical(%s) = %q, %q, %v", v.Public, id, p, ok)
		}
	}
}

func TestCanonicalRefuses(t *testing.T) {
	good, _ := Encode("ins", "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01")
	body := strings.TrimPrefix(good, "ins_")
	for _, s := range []string{
		"", "ins_", "xyz_" + body, "ins_" + body[1:], "ins_" + body + "1", "ins_0" + body[1:],
		"ins_zzzzzzzzzzzzzzzzzzzzzz", // above 2^128
		"ins_YcVfxkQb6JRzqk5kF2tNLw", // exactly 2^128
		"019a0c3e7b107cc08a523f1d2e9b6a01", "{019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01}",
		"urn:uuid:019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01", "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a0g",
	} {
		if _, _, ok := Canonical(s); ok {
			t.Errorf("Canonical(%q) accepted", s)
		}
	}
	if _, ok := Encode("xyz", "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01"); ok {
		t.Error("Encode accepted an unregistered prefix")
	}
}

func TestEqual(t *testing.T) {
	u := "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01"
	ins, _ := Encode("ins", u)
	vol, _ := Encode("vol", u)
	other, _ := Encode("ins", "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a02")
	img, _ := Encode("img", u)
	isn, _ := Encode("isn", u)
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{ins, ins, true},
		{u, ins, true},
		{ins, strings.ToUpper(u), true},
		{u, strings.ToUpper(u), true},
		{ins, vol, false},
		{ins, other, false},
		{u, other, false},
		{"a", "b", false},
		{"name", "name", true},
		{img, isn, true},
		{isn, ins, false},
	} {
		if got := Equal(c.a, c.b); got != c.want {
			t.Errorf("Equal(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The FRN fold's noun table (iam_policy/iam_nouns.json) must agree with the
// registry's IAM nouns.
func TestIAMNounsTableMatchesRegistry(t *testing.T) {
	raw, err := os.ReadFile("../resource/iam_policy/iam_nouns.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Noun, Prefix string }
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	reg := map[string]string{}
	for _, n := range Registry.IAMNouns {
		reg[n.Noun] = n.Prefix
	}
	for _, r := range rows {
		if reg[r.Noun] != r.Prefix {
			t.Errorf("iam_nouns.json %s -> %s, registry says %q", r.Noun, r.Prefix, reg[r.Noun])
		}
	}
}
