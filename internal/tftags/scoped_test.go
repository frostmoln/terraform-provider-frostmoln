package tftags

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

type memPrivate map[string][]byte

func (m memPrivate) GetKey(_ context.Context, k string) ([]byte, diag.Diagnostics) { return m[k], nil }

func (m memPrivate) SetKey(_ context.Context, k string, v []byte) diag.Diagnostics {
	m[k] = v
	return nil
}

// A scoped view keeps its own keys: recording one scope's defaults neither reads
// nor overwrites the resource's own, and a nil private state stays absent.
func TestScopedPrivateStateKeepsItsOwnKeys(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	priv := memPrivate{}
	RecordDefaults(ctx, priv, Defaults{Tags: map[string]string{"own": "1"}}, &diags)
	RecordDefaults(ctx, ScopedWriter(priv, "pool."), Defaults{Tags: map[string]string{"pooled": "1"}}, &diags)

	if got := PriorDefaultKeys(ctx, priv, &diags); len(got) != 1 || got[0] != "own" {
		t.Errorf("resource keys = %v, want [own]", got)
	}
	if got := PriorDefaultKeys(ctx, ScopedReader(priv, "pool."), &diags); len(got) != 1 || got[0] != "pooled" {
		t.Errorf("scoped keys = %v, want [pooled]", got)
	}
	if _, ok := priv["pool."+privateDefaultKeys]; !ok {
		t.Error("the scoped key is not prefixed")
	}
	MarkImported(ctx, ScopedWriter(priv, "pool."), &diags)
	if importPending(ctx, priv, &diags) || len(priv[privateImported]) > 0 {
		t.Error("a scoped import mark reached the resource's own keys")
	}
	if ScopedReader(nil, "x") != nil || ScopedWriter(nil, "x") != nil {
		t.Error("a nil private state must stay nil")
	}
	if diags.HasError() {
		t.Fatal(diags)
	}
}
