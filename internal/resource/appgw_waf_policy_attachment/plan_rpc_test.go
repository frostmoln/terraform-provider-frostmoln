package appgw_waf_policy_attachment_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
	attachment "go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_waf_policy_attachment"
)

// PlanResourceChange is the RPC behind `terraform plan`, and the attachment's
// ModifyPlan is only ONE step of it. The step before it -- the framework's
// MarkComputedNilsAsUnknown -- replaces every Computed attribute that is NULL IN
// THE CONFIGURATION and carries no schema Default with "(known after apply)".
// The eligibility is the config value; the transform never looks at what the
// proposed state carried. On this resource that is id, scope and effective_mode,
// all three, on any plan where the transform runs at all.
//
// The in-package tests call ModifyPlan directly, so they assume where it sits in
// that sequence rather than establishing it. These drive the real RPC, where the
// framework decides the order. Each one says which of the two steps it is
// evidence about -- an assertion that both steps would satisfy is not evidence
// about either.

const (
	attachTypeName = "frostmoln_appgw_waf_policy_attachment"

	attachID   = "agw-1/l-1"
	attachGwID = "agw-1"
	attachLnID = "l-1"
	attachPlID = "wp-ov"
)

func attachObjectType(t *testing.T) tftypes.Object {
	t.Helper()
	var sr fwresource.SchemaResponse
	attachment.NewResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	obj, ok := sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatalf("expected an object type, got %T", sr.Schema.Type().TerraformType(context.Background()))
	}
	return obj
}

// attachValue fills every attribute in the schema with null, then applies the
// overrides. Taking the attribute set from the schema means a new attribute
// cannot silently fall out of these requests.
func attachValue(t *testing.T, obj tftypes.Object, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	attrs := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	for name, v := range overrides {
		if _, ok := obj.AttributeTypes[name]; !ok {
			t.Fatalf("%q is not an attribute of %s", name, attachTypeName)
		}
		attrs[name] = v
	}
	return tftypes.NewValue(obj, attrs)
}

func attachDynamic(t *testing.T, obj tftypes.Object, v tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(obj, v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return &dv
}

// str is a known string value.
func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// planAttachment drives the real PlanResourceChange RPC and returns the planned
// value of one attribute.
func planAttachment(t *testing.T, prior, proposed, config tftypes.Value, attr string) tftypes.Value {
	t.Helper()
	obj := attachObjectType(t)
	server := providerserver.NewProtocol6(provider.New("test")())()
	resp, err := server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         attachTypeName,
		PriorState:       attachDynamic(t, obj, prior),
		ProposedNewState: attachDynamic(t, obj, proposed),
		Config:           attachDynamic(t, obj, config),
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("plan diagnostic: %s -- %s", d.Summary, d.Detail)
		}
	}
	planned, err := resp.PlannedState.Unmarshal(obj)
	if err != nil {
		t.Fatalf("decode planned state: %v", err)
	}
	var attrs map[string]tftypes.Value
	if err := planned.As(&attrs); err != nil {
		t.Fatalf("planned state is not an object: %v", err)
	}
	v, ok := attrs[attr]
	if !ok {
		t.Fatalf("%q missing from the planned state", attr)
	}
	return v
}

// attachConfig is the configuration behind every case here: the three attach
// point attributes plus policy_id, with the computed attributes null, which is
// what makes MarkComputedNilsAsUnknown eligible to touch them at all.
func attachConfig(t *testing.T, obj tftypes.Object, policyID string) tftypes.Value {
	t.Helper()
	return attachValue(t, obj, map[string]tftypes.Value{
		"gateway_id":  str(attachGwID),
		"listener_id": str(attachLnID),
		"policy_id":   str(policyID),
	})
}

// attachPrior is a settled attachment as a refresh left it, with the caller
// choosing what the server resolved effective_mode to.
func attachPrior(t *testing.T, obj tftypes.Object, effectiveMode tftypes.Value) tftypes.Value {
	t.Helper()
	return attachValue(t, obj, map[string]tftypes.Value{
		"id":             str(attachID),
		"gateway_id":     str(attachGwID),
		"listener_id":    str(attachLnID),
		"policy_id":      str(attachPlID),
		"scope":          str("overlay"),
		"effective_mode": effectiveMode,
	})
}

// TestAttachmentPlanRPCMarksComputedNilsUnknownBeforeModifyPlan is the ordering
// claim the resource's own comment makes, established rather than assumed.
//
// 🔴 THE DISCRIMINATOR IS A KNOWN VALUE SURVIVING. If MarkComputedNilsAsUnknown
// ran AFTER the resource's ModifyPlan, it would overwrite what ModifyPlan just
// decided, and the null this pins would come back "(known after apply)" -- the
// unchanged branch would be dead code closing nothing. So this case needs BOTH
// halves to be live at once: a proposed new state that differs from the prior
// one (which is what makes the framework run the transform at all -- see the
// test below), an effective_mode left null in the CONFIGURATION so the transform
// marks it, and an unchanged policy_id so ModifyPlan takes the branch that sets
// a known value.
//
// The `scope` difference is what holds those together. Terraform core would not
// produce it -- a Computed-only attribute keeps its prior value in a proposed
// new state -- and it is here only to isolate the ordering question, which the
// RPC answers for any proposed state it is handed.
//
// `id` carries the other half of the proof. It is Computed-only, absent from the
// configuration and never touched by ModifyPlan, so it comes back unknown if and
// only if the transform ran. Without that check this test would also pass on a
// framework that never ran the transform -- null is what you get either way --
// and the ordering claim it is cited for would be resting on nothing.
func TestAttachmentPlanRPCMarksComputedNilsUnknownBeforeModifyPlan(t *testing.T) {
	t.Parallel()
	obj := attachObjectType(t)

	prior := attachPrior(t, obj, tftypes.NewValue(tftypes.String, nil))
	proposed := attachValue(t, obj, map[string]tftypes.Value{
		"id":          str(attachID),
		"gateway_id":  str(attachGwID),
		"listener_id": str(attachLnID),
		"policy_id":   str(attachPlID), // unchanged: the branch that pins
		"scope":       str("gateway"),  // differs, so the gate lets the transform run
		// effective_mode is left out entirely; what makes it eligible is its null
		// CONFIG value, not the value carried here.
	})

	cfg := attachConfig(t, obj, attachPlID)

	// The transform ran: a Computed attribute ModifyPlan never touches came back
	// unknown. Everything below is only meaningful because of this.
	if id := planAttachment(t, prior, proposed, cfg, "id"); id.IsKnown() {
		t.Fatalf("id = %v, want unknown. MarkComputedNilsAsUnknown did not run on a plan whose "+
			"proposed state differs from prior, so this test cannot say anything about the "+
			"order it runs in relative to ModifyPlan", id)
	}

	got := planAttachment(t, prior, proposed, cfg, "effective_mode")

	if !got.IsKnown() {
		t.Fatalf("effective_mode planned as \"(known after apply)\". Either MarkComputedNilsAsUnknown " +
			"runs AFTER the resource's ModifyPlan -- in which case the unchanged branch cannot " +
			"pin anything and the comment on ModifyPlan describes a hazard it does not close -- " +
			"or ModifyPlan stopped pinning the value")
	}
	if !got.IsNull() {
		t.Fatalf("effective_mode = %v, want the refreshed null", got)
	}
}

// TestAttachmentPlanRPCLeavesAnUnchangedPlanAlone is the other half of the
// ordering test, and it records a REAL LIMIT on the hazard.
//
// The framework runs MarkComputedNilsAsUnknown only when the proposed new state
// DIFFERS from the prior state (server_planresourcechange.go: `if
// !resp.PlannedState.Raw.IsNull() && !resp.PlannedState.Raw.Equal(
// req.PriorState.Raw)`). On a plan that changes nothing the transform never
// runs, so a null effective_mode is already null when ModifyPlan sees it and
// the pin is a no-op rather than a rescue.
//
// `id` is what establishes it, in the opposite direction from the test above. It
// is Computed-only and absent from the configuration, so the transform would
// mark it unknown if it ran, and ModifyPlan never touches it -- a known `id`
// here is direct evidence the transform was skipped. effective_mode alone could
// not show that: ModifyPlan pins it back to the refreshed value either way, so
// it reads null whether the gate held or not.
//
// That is also why this case cannot substitute for the one above: it passes with
// no ModifyPlan at all.
func TestAttachmentPlanRPCLeavesAnUnchangedPlanAlone(t *testing.T) {
	t.Parallel()
	obj := attachObjectType(t)

	prior := attachPrior(t, obj, tftypes.NewValue(tftypes.String, nil))
	cfg := attachConfig(t, obj, attachPlID)

	id := planAttachment(t, prior, prior, cfg, "id")
	if !id.IsKnown() {
		t.Fatalf("id planned as \"(known after apply)\" on a plan that changes nothing, so " +
			"MarkComputedNilsAsUnknown is NOT gated on the proposed state differing from " +
			"prior. ModifyPlan's doc comment argues from that gate that its unchanged branch " +
			"cannot be reached with the states differing; without it that argument fails")
	}
	if !id.Equal(str(attachID)) {
		t.Fatalf("id = %v, want the refreshed %v", id, str(attachID))
	}

	got := planAttachment(t, prior, prior, cfg, "effective_mode")
	if !got.IsKnown() || !got.IsNull() {
		t.Fatalf("effective_mode = %v on a plan with nothing to change, want the refreshed "+
			"null -- anything else is an update reported forever", got)
	}
}

// TestAttachmentPlanRPCRePointsWithoutPromisingTheOldMode RECORDS FRAMEWORK
// BEHAVIOUR. IT DOES NOT GUARD ModifyPlan's UNKNOWN BRANCH, AND CANNOT.
//
// 🔴 EVERY ASSERTION HERE IS ALREADY SATISFIED BEFORE ModifyPlan RUNS. Empty the
// unknown branch -- empty the whole method -- and this test stays green, which
// was checked by mutation rather than assumed. The reason is the transform: on a
// re-point the proposed state differs from prior, so it runs, and effective_mode
// is Computed, absent from the configuration and Default-less, so it is unknown
// before ModifyPlan is reached. The branch re-writes a value already there.
//
// What this test is worth keeping for is the premise the unknown branch's
// justification rests on -- that the framework, unaided, plans a re-pointed
// attachment's effective_mode as unknown. The day that stops being true the
// branch stops being a redundant guard and becomes load-bearing, and this test
// is what notices. The guard on the branch itself is not a test: it is the
// comment on ModifyPlan naming UseStateForUnknown as the edit that makes it
// matter.
func TestAttachmentPlanRPCRePointsWithoutPromisingTheOldMode(t *testing.T) {
	t.Parallel()
	obj := attachObjectType(t)

	prior := attachPrior(t, obj, str("detect"))
	proposed := attachValue(t, obj, map[string]tftypes.Value{
		"id":             str(attachID),
		"gateway_id":     str(attachGwID),
		"listener_id":    str(attachLnID),
		"policy_id":      str("wp-ov2"), // the only non-replacing edit there is
		"scope":          str("overlay"),
		"effective_mode": str("detect"), // the prior value, as core proposes it
	})

	cfg := attachConfig(t, obj, "wp-ov2")

	got := planAttachment(t, prior, proposed, cfg, "effective_mode")
	if got.IsKnown() {
		t.Fatalf("effective_mode planned as %v on a re-pointed attachment. The apply reads the "+
			"NEW policy's mode, so a known promise here ends the run with \"Provider produced "+
			"inconsistent result after apply\". The framework plans this unknown on its own "+
			"today; if it has stopped, ModifyPlan's unknown branch is now the only thing "+
			"preventing that failure and is no longer a redundant guard", got)
	}

	// The same treatment reaches an attribute ModifyPlan never touches, which is
	// what shows the unknown above is the transform's doing, not the branch's.
	if id := planAttachment(t, prior, proposed, cfg, "id"); id.IsKnown() {
		t.Errorf("id = %v, want unknown -- MarkComputedNilsAsUnknown did not run", id)
	}
}
