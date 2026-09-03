package writeonly

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// testSchema mirrors a write-only triple. The names are deliberately generic:
// this package's job is the rule, not any one resource's spelling.
var testSchema = schema.Schema{
	Attributes: map[string]schema.Attribute{
		"thing":            schema.StringAttribute{Optional: true, Sensitive: true},
		"thing_wo":         schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true},
		"thing_wo_version": schema.StringAttribute{Optional: true},
	},
}

var testAttr = Attr{WO: "thing_wo", Version: "thing_wo_version", Legacy: "thing", Subject: "the thing"}

// unset distinguishes "not in this config" from "set to the empty string",
// which is the whole point of several cases below.
const unset = "\x00unset"

func configOf(t *testing.T, values map[string]string, unknown string) tfsdk.Config {
	t.Helper()
	objType, ok := testSchema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema did not render as an object")
	}
	attrs := map[string]tftypes.Value{}
	for name, at := range objType.AttributeTypes {
		switch v, found := values[name]; {
		case name == unknown:
			attrs[name] = tftypes.NewValue(at, tftypes.UnknownValue)
		case !found || v == unset:
			attrs[name] = tftypes.NewValue(at, nil)
		default:
			attrs[name] = tftypes.NewValue(at, v)
		}
	}
	return tfsdk.Config{Schema: testSchema, Raw: tftypes.NewValue(objType, attrs)}
}

func TestReadEnforcesTheTripleAtApplyTime(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		values     map[string]string
		unknown    string
		exactlyOne bool
		wantErr    string // substring of the summary; "" means it must be accepted
		wantValue  string
	}{
		{
			name:      "the happy path is accepted and returns the value",
			values:    map[string]string{"thing_wo": "v", "thing_wo_version": "1"},
			wantValue: "v",
		},
		{
			name:      "neither form set is fine when ExactlyOne is off",
			values:    map[string]string{},
			wantValue: "",
		},
		{
			name:      "the legacy attribute alone is fine",
			values:    map[string]string{"thing": "v"},
			wantValue: "",
		},
		{
			// The one refusal that means a provider bug rather than a bad
			// config. The summary is matched exactly by the provider-level
			// contract test, so it must not drift.
			name:    "an unknown value fails closed",
			values:  map[string]string{"thing_wo_version": "1"},
			unknown: "thing_wo",
			wantErr: "thing_wo Is Unknown At Apply",
		},
		{
			// 🔴 The case LengthAtLeast cannot catch. It returns early on an
			// unknown config value, so a write-only value that is unknown at
			// plan and resolves to "" is never length-checked at all.
			name:    "an empty value is refused",
			values:  map[string]string{"thing_wo": "", "thing_wo_version": "1"},
			wantErr: "thing_wo Is Empty",
		},
		{
			// AlsoRequires returns early on unknown, so this validates clean at
			// plan and the value is then sent once and never again.
			name:    "a value with no version companion is refused",
			values:  map[string]string{"thing_wo": "v"},
			wantErr: "thing_wo_version Is Required With thing_wo",
		},
		{
			// The back-link direction. Its failure is the silent one: nothing
			// is sent and the apply reports success.
			name:    "a version companion with no value is refused",
			values:  map[string]string{"thing_wo_version": "1"},
			wantErr: "thing_wo Is Required With thing_wo_version",
		},
		{
			// ConflictsWith and ExactlyOneOf both skip an unknown path, so this
			// is reachable — and it is the variant that puts the material in
			// state on a config the practitioner believes is write-only.
			name:    "both forms set is refused",
			values:  map[string]string{"thing_wo": "v", "thing_wo_version": "1", "thing": "legacy"},
			wantErr: "thing_wo And thing Are Mutually Exclusive",
		},
		{
			name:       "neither form set is refused when ExactlyOne is on",
			values:     map[string]string{},
			exactlyOne: true,
			wantErr:    "Missing thing_wo Or thing",
		},
		{
			name:       "ExactlyOne still accepts the legacy attribute alone",
			values:     map[string]string{"thing": "v"},
			exactlyOne: true,
			wantValue:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := testAttr
			a.ExactlyOne = tc.exactlyOne

			var diags diag.Diagnostics
			got := a.Read(context.Background(), configOf(t, tc.values, tc.unknown), &diags)

			if tc.wantErr == "" {
				if diags.HasError() {
					t.Fatalf("expected the config to be accepted, got %v", diags.Errors())
				}
				if got.ValueString() != tc.wantValue {
					t.Errorf("value = %q, want %q", got.ValueString(), tc.wantValue)
				}
				return
			}

			if !diags.HasError() {
				t.Fatalf("expected refusal %q, got a clean read of %q", tc.wantErr, got.ValueString())
			}
			var summaries []string
			for _, d := range diags.Errors() {
				summaries = append(summaries, d.Summary())
			}
			if !strings.Contains(strings.Join(summaries, "|"), tc.wantErr) {
				t.Errorf("summaries %v, want one containing %q", summaries, tc.wantErr)
			}
			// Every refusal must fail CLOSED: the caller's guard is
			// `if !value.IsNull()`, so returning the offending value would let
			// a refused config reach the platform anyway.
			if !got.IsNull() {
				t.Errorf("a refused read must return a null value, got %q — the caller's guard is "+
					"`if !IsNull()`, so anything else ships the value the refusal just rejected",
					got.ValueString())
			}
		})
	}
}
