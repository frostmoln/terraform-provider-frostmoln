package tftags

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func offerTagMap(kv ...string) types.Map {
	elems := map[string]attr.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		elems[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, elems)
}

// A data source's tags are every customer key, the offer-reserved namespace
// filtered in any letter case, and an empty map — never null — without any.
func TestOfferTagsValue(t *testing.T) {
	got := OfferTagsValue(map[string]string{"env": "prod", "Frostmoln_Legacy": "x", "frostmoln-note": "y"})
	if !got.Equal(offerTagMap("env", "prod")) {
		t.Errorf("tags = %v, want only env", got)
	}
	if empty := OfferTagsValue(nil); empty.IsNull() || len(empty.Elements()) != 0 {
		t.Errorf("no tags = %v, want an empty map", empty)
	}
}

// OfferTagsToWrite: an update whose tags are unchanged costs no read; a change
// reads the current tags and keeps a key added outside Terraform; a failed read
// is returned for the caller to report.
func TestOfferTagsToWrite(t *testing.T) {
	ctx := context.Background()
	state := offerTagMap("env", "prod")

	reads := 0
	current := func(context.Context) (map[string]string, error) {
		reads++
		return map[string]string{"env": "prod", "portal": "p"}, nil
	}

	var diags diag.Diagnostics
	tags, err := OfferTagsToWrite(ctx, Defaults{}, state, state, state, nil, current, &diags)
	if err != nil || diags.HasError() || tags != nil || reads != 0 {
		t.Fatalf("unchanged: tags=%v err=%v diags=%v reads=%d, want nil and no read", tags, err, diags, reads)
	}

	tags, err = OfferTagsToWrite(ctx, Defaults{}, offerTagMap("env", "dev"), state, state, nil, current, &diags)
	if err != nil || diags.HasError() {
		t.Fatalf("changed: err=%v diags=%v", err, diags)
	}
	if reads != 1 {
		t.Errorf("changed: %d reads, want 1", reads)
	}
	if tags == nil || !Equal(*tags, map[string]string{"env": "dev", "portal": "p"}) {
		t.Errorf("changed: tags = %v, want env=dev plus the portal's key", tags)
	}

	boom := errors.New("boom")
	_, err = OfferTagsToWrite(ctx, Defaults{}, offerTagMap("env", "dev"), state, state, nil,
		func(context.Context) (map[string]string, error) { return nil, boom }, &diags)
	if !errors.Is(err, boom) {
		t.Errorf("failed read: err = %v, want it returned", err)
	}
}
