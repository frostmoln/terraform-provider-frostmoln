package tftags

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestForUpdate(t *testing.T) {
	ctx := context.Background()
	set, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	if d.HasError() {
		t.Fatalf("fixture: %v", d)
	}

	tests := []struct {
		name  string
		in    types.Map
		want  map[string]string
		isNil bool
	}{
		// The regression: a removed `tags` block must CLEAR them. An omitted
		// field means "keep" to every Frostmoln update endpoint, so null has to
		// render as an empty map, not as nil.
		{name: "null clears", in: types.MapNull(types.StringType), want: map[string]string{}},
		{name: "unknown is no opinion", in: types.MapUnknown(types.StringType), isNil: true},
		{name: "set round-trips", in: set, want: map[string]string{"env": "prod"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var diags diag.Diagnostics
			got := ForUpdate(ctx, tt.in, &diags)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if tt.isNil {
				if got != nil {
					t.Fatalf("got %v, want nil so the request carries no opinion", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil — an omitted field means KEEP, so tags could never be cleared")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
