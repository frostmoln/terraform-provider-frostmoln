package instance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// modifyPlanPinnedSecurityGroups refuses, at PLAN time for a CREATE plan, the
// combination the platform refuses inside the saga (01a041f8-98ef):
// subnet_id set without security_groups — null or empty. These tests pin the
// gate itself, in BOTH directions of the state gate:
//
//   - empty state (create): refused — the pinned-subnet-without-security_groups
//     shape fails fast instead of as a failed operation after the 202;
//   - non-empty state (update): NOT refused, because the update-only clear
//     (security_groups = [] or omitted on an existing instance) is the
//     documented, wire-pinned path (behavior_contract_test.go, clearSecurityGroups)
//     — the expert panel rejected the config-only ValidateConfig precisely
//     because it could not tell these apart.
//
// The saga-facing create refusal is pinned separately by
// TestInstanceResource_TFSDKCreateWithPinnedSubnetRefusedForMissingSecurityGroups,
// and the acceptance test renders the plan-time diagnostic end-to-end.

func testPlanSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	return getInstanceSchema(t)
}

func testRaw(t *testing.T, vals map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	tfType := testPlanSchema(t).Schema.Type().TerraformType(context.Background())
	return instanceTFValue(t, tfType, vals)
}

func validatorResource(t *testing.T) *instanceResource {
	// The gate never touches the API, but the resource needs a configured
	// client (orphanInstanceResource always builds one; its Configure()
	// authenticates against /v1/me — answer that one call).
	t.Helper()
	return orphanInstanceResource(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meHandler(w, r)
	})))
}

// planReq builds a ModifyPlanRequest with the given state (a null Raw = a
// create plan) and config.
func planReq(t *testing.T, state, config tftypes.Value) resource.ModifyPlanRequest {
	t.Helper()
	schema := testPlanSchema(t).Schema
	return resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: schema, Raw: state},
		Config: tfsdk.Config{Schema: schema, Raw: config},
		Plan:   tfsdk.Plan{Schema: schema, Raw: state},
	}
}

func TestModifyPlan_PinnedSubnetWithoutSecurityGroupsRefusedOnCreate(t *testing.T) {
	sgType := tftypes.Set{ElementType: tftypes.String}
	cases := []struct {
		name       string
		subnet     tftypes.Value
		sg         tftypes.Value
		wantRefuse bool
	}{
		{
			name:       "subnet set, security_groups null",
			subnet:     tftypes.NewValue(tftypes.String, "subnet-123"),
			sg:         tftypes.NewValue(sgType, nil),
			wantRefuse: true,
		},
		{
			name:       "subnet set (computed reference), security_groups null",
			subnet:     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			sg:         tftypes.NewValue(sgType, nil),
			wantRefuse: true,
		},
		{
			name:       "subnet set, security_groups explicitly empty",
			subnet:     tftypes.NewValue(tftypes.String, "subnet-123"),
			sg:         tftypes.NewValue(sgType, []tftypes.Value{}),
			wantRefuse: true,
		},
		{
			name:       "subnet set, security_groups present",
			subnet:     tftypes.NewValue(tftypes.String, "subnet-123"),
			sg:         tftypes.NewValue(sgType, []tftypes.Value{tftypes.NewValue(tftypes.String, "sg-web")}),
			wantRefuse: false,
		},
		{
			name:       "no subnet, security_groups null (the legal omission)",
			subnet:     tftypes.NewValue(tftypes.String, nil),
			sg:         tftypes.NewValue(sgType, nil),
			wantRefuse: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validatorResource(t)

			cfgVal := testRaw(t, map[string]tftypes.Value{
				"subnet_id":       tc.subnet,
				"security_groups": tc.sg,
			})

			resp := &resource.ModifyPlanResponse{}
			req := planReq(t, tftypes.NewValue(cfgVal.Type(), nil), cfgVal)
			r.modifyPlanPinnedSecurityGroups(context.Background(), req, resp)

			if tc.wantRefuse {
				if !resp.Diagnostics.HasError() {
					t.Fatal("expected the pinned-subnet-without-security_groups refusal on a create plan")
				}
				text := orphanInstanceDiagText(resp.Diagnostics)
				for _, want := range []string{
					"security_groups",
					"requires at least one security group",
					"security_group_ids is required",
				} {
					if !strings.Contains(text, want) {
						t.Errorf("diagnostic must name %q:\n%s", want, text)
					}
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatalf("expected a valid combination to pass:\n%s", orphanInstanceDiagText(resp.Diagnostics))
			}
		})
	}
}

// THE OTHER DIRECTION of the state gate — the case that rejected the
// config-only ValidateConfig: an UPDATE plan for an EXISTING pinned instance
// where the caller clears security_groups ([axis] attribute omitted, the
// documented clear) is NOT refused. The instance exists; Update PUTs
// clearSecurityGroups and the platform honors the clear-to-default-drop.
func TestModifyPlan_UpdateClearOfSecurityGroupsNotRefused(t *testing.T) {
	r := validatorResource(t)
	sgType := tftypes.Set{ElementType: tftypes.String}

	state := testRaw(t, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "inst-1"),
		"subnet_id":       tftypes.NewValue(tftypes.String, "subnet-123"),
		"security_groups": tftypes.NewValue(sgType, []tftypes.Value{tftypes.NewValue(tftypes.String, "sg-web")}),
	})
	config := testRaw(t, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "inst-1"),
		"subnet_id":       tftypes.NewValue(tftypes.String, "subnet-123"),
		"security_groups": tftypes.NewValue(sgType, []tftypes.Value{}),
	})

	req := planReq(t, state, config)
	req.Plan = tfsdk.Plan{Schema: testPlanSchema(t).Schema, Raw: config}
	resp := &resource.ModifyPlanResponse{}
	r.modifyPlanPinnedSecurityGroups(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("the documented update-only clear of security_groups must not be refused:\n%s", orphanInstanceDiagText(resp.Diagnostics))
	}
}

func TestModifyPlan_UnknownSecurityGroupsBails(t *testing.T) {
	sgType := tftypes.Set{ElementType: tftypes.String}
	r := validatorResource(t)

	cfg := testRaw(t, map[string]tftypes.Value{
		"subnet_id":       tftypes.NewValue(tftypes.String, "subnet-123"),
		"security_groups": tftypes.NewValue(sgType, tftypes.UnknownValue),
	})

	req := planReq(t, tftypes.NewValue(cfg.Type(), nil), cfg)
	resp := &resource.ModifyPlanResponse{}
	r.modifyPlanPinnedSecurityGroups(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an unknown security_groups may satisfy the constraint once known — only provable violations refuse:\n%s", orphanInstanceDiagText(resp.Diagnostics))
	}
}
