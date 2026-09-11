package timeouts

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestResolveNilModelFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	got, err := (*Model)(nil).Resolve(Uniform(30 * time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != (Budgets{Create: 30 * time.Minute, Update: 30 * time.Minute, Delete: 30 * time.Minute}) {
		t.Fatalf("expected defaults for every verb, got %+v", got)
	}
}

func TestResolveEmptyBlockFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	got, err := (&Model{}).Resolve(Budgets{Create: 10 * time.Minute, Update: 20 * time.Minute, Delete: 30 * time.Minute})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Budgets{Create: 10 * time.Minute, Update: 20 * time.Minute, Delete: 30 * time.Minute}
	if got != want {
		t.Fatalf("want %+v, got %+v", want, got)
	}
}

func TestResolveOverridesWinPerVerb(t *testing.T) {
	t.Parallel()

	got, err := (&Model{
		Create: types.StringValue("45m"),
		Update: types.StringValue("1h30m"),
		Delete: types.StringValue("2h"),
	}).Resolve(Uniform(5 * time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Budgets{Create: 45 * time.Minute, Update: 90 * time.Minute, Delete: 2 * time.Hour}
	if got != want {
		t.Fatalf("want %+v, got %+v", want, got)
	}
}

func TestResolveUnknownCountsAsUnset(t *testing.T) {
	t.Parallel()

	got, err := (&Model{Create: types.StringUnknown()}).Resolve(Uniform(7 * time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Create != 7*time.Minute {
		t.Fatalf("expected an unknown value to fall back to the default, got %s", got.Create)
	}
}

func TestResolveInvalidDurationNamesTheVerb(t *testing.T) {
	t.Parallel()

	_, err := (&Model{Update: types.StringValue("banana")}).Resolve(Uniform(time.Minute))
	if err == nil {
		t.Fatal("expected an error for a non-duration value")
	}
	if !strings.Contains(err.Error(), "timeouts.update") {
		t.Fatalf("error %q does not name the offending verb", err)
	}
}

func TestUniformBudgetsEveryVerb(t *testing.T) {
	t.Parallel()

	got := Uniform(5 * time.Second)
	if got.Create != 5*time.Second || got.Update != 5*time.Second || got.Delete != 5*time.Second {
		t.Fatalf("Uniform(5s) did not budget every verb: %+v", got)
	}
}

func TestSchemaShape(t *testing.T) {
	t.Parallel()

	b := Schema()
	if len(b.Attributes) != 3 {
		t.Fatalf("expected create/update/delete attributes, got %d", len(b.Attributes))
	}
	if len(b.Blocks) != 0 {
		t.Fatalf("the timeouts block carries no sub-blocks, got %d", len(b.Blocks))
	}
	for _, verb := range []string{"create", "update", "delete"} {
		attr, ok := b.Attributes[verb]
		if !ok {
			t.Fatalf("missing %s attribute", verb)
		}
		strAttr, ok := attr.(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a string attribute: %T", verb, attr)
		}
		if !attr.IsOptional() {
			t.Fatalf("%s must be optional", verb)
		}
		if attr.IsComputed() || attr.IsRequired() {
			t.Fatalf("%s must not be computed or required", verb)
		}
		if len(strAttr.Validators) != 1 {
			t.Fatalf("%s must carry exactly the duration validator", verb)
		}
	}
}

func TestDurationValidator(t *testing.T) {
	t.Parallel()

	v := Duration()

	for _, tc := range []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"valid minutes", types.StringValue("90m"), false},
		{"valid compound", types.StringValue("2h30m"), false},
		{"valid seconds", types.StringValue("30s"), false},
		{"null passes", types.StringNull(), false},
		{"unknown passes", types.StringUnknown(), false},
		{"unitless rejected", types.StringValue("90"), true},
		{"nonsense rejected", types.StringValue("banana"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validator.StringRequest{ConfigValue: tc.value}
			resp := &validator.StringResponse{}
			v.ValidateString(t.Context(), req, resp)
			if tc.wantErr && !resp.Diagnostics.HasError() {
				t.Fatalf("expected an error for %q", tc.value.ValueString())
			}
			if !tc.wantErr && resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error for %q: %+v", tc.value, resp.Diagnostics)
			}
		})
	}
}
