package tftags

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A refresh that cannot read the tags keeps what state holds and warns; a
// create's unknown tags_all becomes the set the create sent; an import's null
// tags_all stays null. Never an error: the object itself was read.
func TestKeepOfferTagsWhenUnavailable(t *testing.T) {
	ctx := context.Background()

	var diags diag.Diagnostics
	tags, all := KeepOfferTagsWhenUnavailable(ctx, "the cache", offerTagMap("env", "prod"),
		offerTagMap("env", "prod", "portal", "p"), map[string]string{"ignored": "x"}, &diags)
	if !tags.Equal(offerTagMap("env", "prod")) || !all.Equal(offerTagMap("env", "prod", "portal", "p")) {
		t.Errorf("refresh: tags=%v tags_all=%v, want the prior values kept", tags, all)
	}
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Errorf("refresh: diagnostics = %v, want exactly one warning", diags)
	}

	diags = nil
	tags, all = KeepOfferTagsWhenUnavailable(ctx, "the cache", types.MapUnknown(types.StringType),
		types.MapUnknown(types.StringType), map[string]string{"env": "prod"}, &diags)
	if !tags.IsNull() || !all.Equal(offerTagMap("env", "prod")) {
		t.Errorf("create: tags=%v tags_all=%v, want null and the sent set", tags, all)
	}

	diags = nil
	_, all = KeepOfferTagsWhenUnavailable(ctx, "the cache", types.MapNull(types.StringType),
		types.MapUnknown(types.StringType), nil, &diags)
	if all.IsNull() || all.IsUnknown() || len(all.Elements()) != 0 {
		t.Errorf("create without tags: tags_all=%v, want an empty known map", all)
	}

	diags = nil
	_, all = KeepOfferTagsWhenUnavailable(ctx, "the cache", types.MapNull(types.StringType),
		types.MapNull(types.StringType), nil, &diags)
	if !all.IsNull() {
		t.Errorf("import: tags_all=%v, want it to stay null", all)
	}
}
