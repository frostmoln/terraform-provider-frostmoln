package tftags

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestFromAPI(t *testing.T) {
	ctx := context.Background()
	prior, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	if d.HasError() {
		t.Fatalf("fixture: %v", d)
	}
	emptyPrior := types.MapValueMust(types.StringType, map[string]attr.Value{})

	tests := []struct {
		name    string
		api     map[string]string
		prior   types.Map
		want    map[string]string
		wantNil bool // want a null map
	}{
		// Drift: the API's values win over whatever prior held.
		{name: "api values replace prior", api: map[string]string{"env": "staging"}, prior: prior, want: map[string]string{"env": "staging"}},
		{name: "api values over null prior", api: map[string]string{"env": "staging"}, prior: types.MapNull(types.StringType), want: map[string]string{"env": "staging"}},
		// The bucket regression: an untagged read over a tagged prior must NOT
		// keep the prior's values, or an out-of-band removal is invisible.
		{name: "absent over tagged prior is empty, not prior", api: nil, prior: prior, want: map[string]string{}},
		{name: "empty over tagged prior is empty, not prior", api: map[string]string{}, prior: prior, want: map[string]string{}},
		// `tags = {}` round-trips; an unset attribute stays null.
		{name: "absent over empty prior stays empty", api: nil, prior: emptyPrior, want: map[string]string{}},
		{name: "empty over empty prior stays empty", api: map[string]string{}, prior: emptyPrior, want: map[string]string{}},
		{name: "absent over null prior stays null", api: nil, prior: types.MapNull(types.StringType), wantNil: true},
		{name: "empty over null prior stays null", api: map[string]string{}, prior: types.MapNull(types.StringType), wantNil: true},
		{name: "absent over unknown prior is null", api: nil, prior: types.MapUnknown(types.StringType), wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var diags diag.Diagnostics
			got := FromAPI(ctx, tt.api, tt.prior, &diags)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if got.IsUnknown() {
				t.Fatal("got an unknown map; state must never hold unknown after a read")
			}
			if tt.wantNil {
				if !got.IsNull() {
					t.Fatalf("got %v, want null", got)
				}
				return
			}
			if got.IsNull() {
				t.Fatalf("got null, want %v", tt.want)
			}
			gotMap := map[string]string{}
			diags.Append(got.ElementsAs(ctx, &gotMap, false)...)
			if len(gotMap) != len(tt.want) {
				t.Fatalf("got %v, want %v", gotMap, tt.want)
			}
			for k, v := range tt.want {
				if gotMap[k] != v {
					t.Errorf("key %q = %q, want %q", k, gotMap[k], v)
				}
			}
		})
	}
}

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
