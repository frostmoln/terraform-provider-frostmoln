package tftags

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func validateOfferTags(t *testing.T, m types.Map) []string {
	t.Helper()
	var resp validator.MapResponse
	OfferTagsValidator().ValidateMap(context.Background(), validator.MapRequest{Path: path.Root("tags"), ConfigValue: m}, &resp)
	var out []string
	for _, d := range resp.Diagnostics.Errors() {
		out = append(out, d.Detail())
	}
	return out
}

func strMap(kv map[string]string) types.Map {
	elems := map[string]attr.Value{}
	for k, v := range kv {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

func TestOfferTagsValidator(t *testing.T) {
	cases := []struct {
		name string
		tags map[string]string
		want string // "" = valid
	}{
		{"valid", map[string]string{"env": "prod", "a.b:c/d-e_f": "x", "k": ""}, ""},
		{"reserved underscore", map[string]string{"frostmoln_type": "x"}, "reserved"},
		{"reserved hyphen any case", map[string]string{"Frostmoln-Note": "x"}, "reserved"},
		{"key too long", map[string]string{strings.Repeat("k", 65): "x"}, "1 to 64 bytes"},
		{"key 64 bytes ok", map[string]string{strings.Repeat("k", 64): "x"}, ""},
		{"key charset", map[string]string{"bad key": "x"}, "may contain only"},
		{"key edge", map[string]string{"-env": "x"}, "may contain only"},
		{"value 256 bytes ok", map[string]string{"k": strings.Repeat("v", 256)}, ""},
		{"value bytes not runes", map[string]string{"k": strings.Repeat("å", 129)}, "258 bytes"},
		{"control char", map[string]string{"k": "a\nb"}, "control"},
		{"bidi override", map[string]string{"k": "a\u202eb"}, "control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateOfferTags(t, strMap(tc.tags))
			if tc.want == "" && len(got) != 0 {
				t.Fatalf("want valid, got %v", got)
			}
			if tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)) {
				t.Fatalf("want one error containing %q, got %v", tc.want, got)
			}
		})
	}

	t.Run("more than 32 keys", func(t *testing.T) {
		many := map[string]string{}
		for i := 0; i < 33; i++ {
			many["k"+strings.Repeat("x", i)] = "v"
		}
		got := validateOfferTags(t, strMap(many))
		if len(got) != 1 || !strings.Contains(got[0], "at most 32") {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("null and unknown are skipped", func(t *testing.T) {
		if got := validateOfferTags(t, types.MapNull(types.StringType)); len(got) != 0 {
			t.Fatal(got)
		}
		if got := validateOfferTags(t, types.MapUnknown(types.StringType)); len(got) != 0 {
			t.Fatal(got)
		}
		unknownValue := types.MapValueMust(types.StringType, map[string]attr.Value{"env": types.StringUnknown()})
		if got := validateOfferTags(t, unknownValue); len(got) != 0 {
			t.Fatal(got)
		}
	})
}

// default_tags can never carry an offer-reserved key: the provider refuses both
// spellings, in any case, so no offer resource needs to warn about one.
func TestDefaultTagsRefuseEveryOfferReservedKey(t *testing.T) {
	v := "x"
	for _, k := range []string{"frostmoln_a", "frostmoln-a", "FROSTMOLN-a", "Frostmoln_A"} {
		if !IsOfferReservedKey(k) {
			t.Fatalf("%s should be offer-reserved", k)
		}
		if p := CheckDefaultTag(k, &v); p == nil || p.Rule != "reserved_prefix" {
			t.Errorf("default_tags accepted the offer-reserved key %q: %+v", k, p)
		}
	}
}
