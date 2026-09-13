package tftags

import (
	"fmt"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// The vectors are identity's (internal/domain/tag_settings_test.go,
// ValidateTenantDefaultTags): the provider refuses exactly what identity
// refuses for a tenant's default tags, rule for rule.

func TestCheckDefaultTagAcceptsTheIntersection(t *testing.T) {
	ok := map[string]string{
		"env":                   "prod",
		"cost-center":           "eu.team:42",
		"a":                     "",
		"Team_Name.v2":          "Platform Ops",
		"k1":                    "user@example.com",
		"path":                  "a/b/c",
		"eq":                    "x=y+z",
		strings.Repeat("k", 64): strings.Repeat("v", 255),
		// near-misses of the reserved set are ordinary keys
		"frostmoln": "v", "osx": "v", "nova": "v", "instance": "v", "request-idx": "v", "acls": "v", "my-acl": "v",
	}
	for k, v := range ok {
		if p := CheckDefaultTag(k, strp(v)); p != nil {
			t.Errorf("%q=%q refused: %s", k, v, p.Detail)
		}
	}
}

func TestCheckDefaultTagReservedKeys(t *testing.T) {
	for key, rule := range map[string]string{
		"frostmoln_type": "reserved_prefix", "frostmoln-owner": "reserved_prefix",
		"Frostmoln_Type": "reserved_prefix", "FROSTMOLN-X": "reserved_prefix",
		"os_type": "reserved_prefix", "instance_name": "reserved_prefix", "nova_x": "reserved_prefix",
		"OS_Type":    "reserved_prefix",
		"request-id": "reserved_key", "customer-id": "reserved_key", "project-id": "reserved_key",
		"Customer-Id": "reserved_key",
		"tenant-id":   "reserved_key", "created-at": "reserved_key", "acl": "reserved_key",
		"storage-class": "reserved_key", "quota-bytes": "reserved_key", "cors-config": "reserved_key",
		"ACL": "reserved_key",
	} {
		p := CheckDefaultTag(key, strp("v"))
		if p == nil || p.Rule != rule {
			t.Errorf("%q: got %+v, want rule %s", key, p, rule)
			continue
		}
		if !strings.Contains(p.Detail, key) {
			t.Errorf("%q: the refusal must name the key: %s", key, p.Detail)
		}
	}
}

func TestCheckDefaultTagKeyShape(t *testing.T) {
	for key, rule := range map[string]string{
		"":                      "key_length",
		strings.Repeat("k", 65): "key_length",
		"team/owner":            "key_charset", // instance metadata keys refuse /
		"my key":                "key_charset",
		"user@x":                "key_charset",
		"a=b":                   "key_charset",
		"a+b":                   "key_charset",
		"-lead":                 "key_charset",
		"trail.":                "key_charset",
		"_x":                    "key_charset",
		"miljö":                 "key_charset",
		"k\x00":                 "key_charset",
	} {
		if p := CheckDefaultTag(key, strp("v")); p == nil || p.Rule != rule {
			t.Errorf("%q: got %+v, want rule %s", key, p, rule)
		}
	}
}

func TestCheckDefaultTagValueShape(t *testing.T) {
	for value, rule := range map[string]string{
		strings.Repeat("v", 256): "value_length",
		`say "hi"`:               "value_charset",
		"it's":                   "value_charset",
		`a\b`:                    "value_charset",
		"a,b":                    "value_charset",
		"ünï":                    "value_charset",
		"tab\there":              "value_charset",
		"a\u202eb":               "value_charset",
		"#hash":                  "value_charset",
	} {
		if p := CheckDefaultTag("env", strp(value)); p == nil || p.Rule != rule {
			t.Errorf("value %q: got %+v, want rule %s", value, p, rule)
		}
	}
	// An unknown value is checked by the apply-time configure; its key now.
	if p := CheckDefaultTag("env", nil); p != nil {
		t.Errorf("an unknown value was refused: %+v", p)
	}
	if p := CheckDefaultTag("team/owner", nil); p == nil {
		t.Error("the key of an unknown value must still be checked")
	}
}

func TestTooManyDefaultTagsDetail(t *testing.T) {
	d := TooManyDefaultTagsDetail(MaxDefaultTags + 1)
	if !strings.Contains(d, fmt.Sprint(MaxDefaultTags+1)) || !strings.Contains(d, fmt.Sprint(MaxDefaultTags)) {
		t.Errorf("the refusal must give the count and the maximum: %s", d)
	}
}
