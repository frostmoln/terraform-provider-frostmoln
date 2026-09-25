package s3_credential

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ipWhitelistValidators returns the validators the resource SCHEMA attaches to
// ip_whitelist, so the tests below exercise the wiring and not just the type: a
// validator that exists but is not attached would stop nothing at plan time.
func ipWhitelistValidators(t *testing.T) []validator.List {
	t.Helper()
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	attrib, ok := resp.Schema.Attributes["ip_whitelist"].(schema.ListAttribute)
	if !ok {
		t.Fatalf("ip_whitelist is not a schema.ListAttribute: %T", resp.Schema.Attributes["ip_whitelist"])
	}
	if len(attrib.Validators) == 0 {
		t.Fatal("ip_whitelist has no validators: a non-empty list would plan destroy-then-create and lose the key")
	}
	return attrib.Validators
}

func validateIPWhitelist(t *testing.T, v types.List) *validator.ListResponse {
	t.Helper()
	resp := &validator.ListResponse{}
	for _, lv := range ipWhitelistValidators(t) {
		lv.ValidateList(context.Background(), validator.ListRequest{
			Path:        path.Root("ip_whitelist"),
			ConfigValue: v,
		}, resp)
	}
	return resp
}

func TestIPWhitelistValidator_RefusesNonEmpty(t *testing.T) {
	for name, v := range map[string]types.List{
		"one cidr": types.ListValueMust(types.StringType, []attr.Value{types.StringValue("203.0.113.0/24")}),
		"two":      types.ListValueMust(types.StringType, []attr.Value{types.StringValue("10.0.0.1"), types.StringValue("10.0.0.2")}),
		// Known length, unknown element: still a non-empty list.
		"unknown element": types.ListValueMust(types.StringType, []attr.Value{types.StringUnknown()}),
	} {
		t.Run(name, func(t *testing.T) {
			resp := validateIPWhitelist(t, v)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected a plan-time error for a non-empty ip_whitelist")
			}
			d := resp.Diagnostics.Errors()[0]
			if !strings.Contains(d.Summary(), "temporarily unavailable") {
				t.Errorf("summary should say allow-lists are temporarily unavailable, got %q", d.Summary())
			}
			if !strings.Contains(d.Detail(), "Remove ip_whitelist") {
				t.Errorf("detail should tell the user to remove ip_whitelist, got %q", d.Detail())
			}
		})
	}
}

func TestIPWhitelistValidator_AllowsEmptyNullUnknown(t *testing.T) {
	for name, v := range map[string]types.List{
		"null":    types.ListNull(types.StringType),
		"unknown": types.ListUnknown(types.StringType),
		"empty":   types.ListValueMust(types.StringType, []attr.Value{}),
	} {
		t.Run(name, func(t *testing.T) {
			if resp := validateIPWhitelist(t, v); resp.Diagnostics.HasError() {
				t.Fatalf("expected no error, got %v", resp.Diagnostics.Errors())
			}
		})
	}
}
