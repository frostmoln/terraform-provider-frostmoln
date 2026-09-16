package unenacted

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The request carries only the fields the validators read (ConfigValue and
// Path); the Config zero value is untouched by this package the same way the
// shared plan-modifier replays only touch their accessor fields (see
// planmodifier_order_test.go).

func TestStringRefusesKnownValue(t *testing.T) {
	resp := &validator.StringResponse{}
	String("stored, never applied", "the platform constraint text").ValidateString(
		context.Background(),
		validator.StringRequest{Path: path.Root("parameter_group_id"), ConfigValue: types.StringValue("pg-1")},
		resp,
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the known value to be refused")
	}
	if got, want := resp.Diagnostics.Errors()[0].Summary(), "stored, never applied"; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "the platform constraint text") {
		t.Errorf("expected the constraint text in the detail, got %q", resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestStringAllowsNullAndUnknown(t *testing.T) {
	validatorUnderTest := String("stored, never applied", "the platform constraint text")

	resp := &validator.StringResponse{}
	validatorUnderTest.ValidateString(context.Background(),
		validator.StringRequest{Path: path.Root("x"), ConfigValue: types.StringNull()}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected null to pass, got %v", resp.Diagnostics.Errors())
	}

	resp = &validator.StringResponse{}
	validatorUnderTest.ValidateString(context.Background(),
		validator.StringRequest{Path: path.Root("x"), ConfigValue: types.StringUnknown()}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected unknown to pass (the CRUD belt re-checks at apply), got %v", resp.Diagnostics.Errors())
	}
}

func TestBoolTrueRefusesOnlyTrue(t *testing.T) {
	validatorUnderTest := BoolTrue("TLS is not provisioned", "the platform constraint text")

	resp := &validator.BoolResponse{}
	validatorUnderTest.ValidateBool(context.Background(),
		validator.BoolRequest{Path: path.Root("tls_enabled"), ConfigValue: types.BoolValue(true)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected true to be refused")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "the platform constraint text") {
		t.Errorf("expected the constraint text in the detail, got %q", resp.Diagnostics.Errors()[0].Detail())
	}

	for name, value := range map[string]types.Bool{
		"false":   types.BoolValue(false),
		"null":    types.BoolNull(),
		"unknown": types.BoolUnknown(),
	} {
		resp := &validator.BoolResponse{}
		validatorUnderTest.ValidateBool(context.Background(),
			validator.BoolRequest{Path: path.Root("tls_enabled"), ConfigValue: value}, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("expected %s to pass, got %v", name, resp.Diagnostics.Errors())
		}
	}
}
