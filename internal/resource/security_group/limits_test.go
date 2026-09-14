package security_group

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// network validates a security group's name (max 234) and description (max
// 1024) in CHARACTERS. The plan-time checks must agree exactly: a byte count
// would refuse a non-ASCII name the platform accepts, and no check at all
// turned a 235-character name into an operation that fails after the create
// was accepted.
func TestSecurityGroupLengthLimitsCountCharacters(t *testing.T) {
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)

	cases := []struct {
		attr    string
		value   string
		wantErr bool
	}{
		{"name", strings.Repeat("å", 234), false}, // 468 bytes, 234 characters
		{"name", strings.Repeat("a", 235), true},
		{"name", "", true},
		{"description", strings.Repeat("ö", 1024), false}, // 2048 bytes
		{"description", strings.Repeat("a", 1025), true},
	}
	for _, tc := range cases {
		attr, ok := resp.Schema.Attributes[tc.attr].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a string attribute", tc.attr)
		}
		if len(attr.Validators) == 0 {
			t.Fatalf("%s has no validators", tc.attr)
		}
		var failed bool
		for _, v := range attr.Validators {
			out := &validator.StringResponse{}
			v.ValidateString(context.Background(), validator.StringRequest{
				Path:        path.Root(tc.attr),
				ConfigValue: types.StringValue(tc.value),
			}, out)
			failed = failed || out.Diagnostics.HasError()
		}
		if failed != tc.wantErr {
			t.Errorf("%s of %d characters: error=%v, want %v", tc.attr, len([]rune(tc.value)), failed, tc.wantErr)
		}
	}
}
