package appgw_waf_policy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestEffectiveModeIsWhatTheEngineDoes.
//
// 🔴 THE DEFECT, IN ONE TABLE. An overlay policy set to mode "inherit" under a
// BLOCKING gateway policy IS BLOCKING, and its `mode` is the string "inherit".
// A configuration or a check written as `mode == "block"` is therefore FALSE
// for a policy whose users' requests are being refused -- which is why
// effective_mode exists and why it, not mode, is what this provider reports.
func TestEffectiveModeIsWhatTheEngineDoes(t *testing.T) {
	cases := []struct {
		name          string
		mode          string
		effectiveMode string
		// want is "" when the mode in force cannot be determined, which the
		// attribute must render as NULL.
		want string
	}{
		{"an inheriting overlay under a blocking gateway BLOCKS", modeInherit, modeBlock, modeBlock},
		{"an inheriting overlay under a detecting gateway detects", modeInherit, modeDetect, modeDetect},
		{"an explicit mode is its own effective mode", modeBlock, modeBlock, modeBlock},
		// 🔴 NEVER INVENT A RESOLUTION -- SURFACE UNKNOWN. Resolving "inherit"
		// needs the GATEWAY policy's mode, which a client reading one policy
		// does not have. This case previously asserted the authored token,
		// which pinned the wrong rule: it let the literal string "inherit" sit
		// in an attribute documented as "mode with inherit RESOLVED", so
		// `self.effective_mode == "block" ? 1 : 0` took a branch confidently on
		// a value that answers nothing.
		{"an unresolved inherit is UNKNOWN, and renders as null", modeInherit, "", ""},
		{"a server that fills nothing still resolves an explicit mode", modeBlock, "", modeBlock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveMode(tc.mode, tc.effectiveMode); got != tc.want {
				t.Errorf("effectiveMode(%q, %q) = %q, want %q", tc.mode, tc.effectiveMode, got, tc.want)
			}
			var m PolicyModel
			m.fromAPI(&apiPolicy{
				ID: "wp-1", GatewayID: "agw-1", Name: "api",
				Scope: scopeOverlay, Mode: tc.mode, EffectiveMode: tc.effectiveMode,
			})
			if tc.want == "" {
				if !m.EffectiveMode.IsNull() {
					t.Errorf("effective_mode = %q, want null -- the mode in force is unknown",
						m.EffectiveMode.ValueString())
				}
			} else if m.EffectiveMode.ValueString() != tc.want {
				t.Errorf("effective_mode = %q, want %q", m.EffectiveMode.ValueString(), tc.want)
			}
			// "inherit" is an AUTHORED value and never an answer to "what does
			// the engine do".
			if m.EffectiveMode.ValueString() == modeInherit {
				t.Errorf("effective_mode carries the authored token %q", modeInherit)
			}
			// The AUTHORED mode is preserved untouched: a plan compares it
			// against config, and overwriting it with the resolution would make
			// an inheriting overlay show a permanent diff.
			if m.Mode.ValueString() != tc.mode {
				t.Errorf("mode = %q, want the authored %q", m.Mode.ValueString(), tc.mode)
			}
		})
	}
}

// TestScopeDefaultsToGatewayWhenTheServerOmitsIt. The server omits the default,
// and reporting null for it would make a config that never mentioned scope show
// a permanent diff -- and one that set it to "gateway" plan a replacement of a
// policy that never changed.
func TestScopeDefaultsToGatewayWhenTheServerOmitsIt(t *testing.T) {
	var m PolicyModel
	m.fromAPI(&apiPolicy{ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: modeDetect})
	if m.Scope.ValueString() != scopeGateway {
		t.Fatalf("scope = %v, want %q for an omitted value", m.Scope, scopeGateway)
	}
	var overlay PolicyModel
	overlay.fromAPI(&apiPolicy{ID: "wp-2", Scope: scopeOverlay, Mode: modeInherit})
	if overlay.Scope.ValueString() != scopeOverlay {
		t.Fatalf("scope = %v, want overlay", overlay.Scope)
	}
}

// TestScopeReachesTheCreateBodyAndNeverTheUpdateBody.
//
// Scope is create-only. Sending it on an update would be a client asserting
// something the API has no field for; the counterpart is that it MUST reach the
// create, or every overlay a practitioner declares is silently made a gateway
// policy carrying the managed ruleset.
func TestScopeReachesTheCreateBodyAndNeverTheUpdateBody(t *testing.T) {
	var createBody map[string]any
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&createBody)
		w.WriteHeader(http.StatusCreated)
		f := wpFixture()
		f.Scope, f.Mode, f.EffectiveMode = scopeOverlay, modeInherit, modeBlock
		f.ParanoiaLevel, f.AnomalyScoreThreshold, f.CRSVersion = 0, 0, ""
		_ = json.NewEncoder(w).Encode(f)
	})
	pr := &policyResource{client: c}

	plan := wpModel()
	plan.Scope = types.StringValue(scopeOverlay)
	plan.Mode = types.StringValue(modeInherit)
	createResp := resource.CreateResponse{State: emptyState(t)}
	pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, plan)}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	if createBody["scope"] != scopeOverlay {
		t.Fatalf("scope did not reach the create body: %v", createBody)
	}
	if _, sent := createBody["effectiveMode"]; sent {
		t.Errorf("effectiveMode is read-only and must never be sent: %v", createBody)
	}
	var created PolicyModel
	createResp.State.Get(context.Background(), &created)
	if created.EffectiveMode.ValueString() != modeBlock {
		t.Fatalf("effective_mode = %v; the server said this inheriting overlay is BLOCKING",
			created.EffectiveMode)
	}

	// Now an update that changes only the mode.
	var updateBody map[string]any
	c2, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&updateBody)
		f := wpFixture()
		f.Scope, f.Mode, f.EffectiveMode = scopeOverlay, modeInherit, modeBlock
		_ = json.NewEncoder(w).Encode(f)
	})
	pr2 := &policyResource{client: c2}

	var state PolicyModel
	state.fromAPI(&apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "api", Scope: scopeOverlay,
		Mode: modeDetect, EffectiveMode: modeDetect, FailMode: "open",
		RequestBodyLimitBytes: 8192,
	})
	next := state
	next.Mode = types.StringValue(modeInherit)
	updResp := resource.UpdateResponse{State: stateOf(t, state)}
	pr2.Update(context.Background(), resource.UpdateRequest{
		Plan: planOf(t, next), State: stateOf(t, state)}, &updResp)
	if updResp.Diagnostics.HasError() {
		t.Fatalf("update: %v", updResp.Diagnostics.Errors())
	}
	for _, forbidden := range []string{"scope", "effectiveMode"} {
		if _, sent := updateBody[forbidden]; sent {
			t.Errorf("%s reached the update body; it is create-only or read-only: %v",
				forbidden, updateBody)
		}
	}
	// Moving to "inherit" under a blocking gateway starts refusing requests,
	// and a practitioner reading only their own diff sees a word that looks
	// like a relaxation.
	if !warningsMention(updResp.Diagnostics.Warnings(), "gateway policy") {
		t.Errorf("moving to inherit did not warn about what it now resolves to: %v",
			updResp.Diagnostics.Warnings())
	}
}

// TestValidateConfigMirrorsTheServerConstraints.
//
// The server holds a database check -- mode IN ('detect','block') OR
// (mode='inherit' AND scope='overlay') -- and refuses the managed-ruleset dials
// on an overlay, which is not compiled with that ruleset. Both are enforced at
// PLAN time so the practitioner reads the reason instead of a 400 naming a JSON
// field partway through an apply.
func TestValidateConfigMirrorsTheServerConstraints(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m PolicyModel) []string {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p)}, &resp)
		var msgs []string
		for _, d := range resp.Diagnostics.Errors() {
			msgs = append(msgs, d.Summary()+" "+d.Detail())
		}
		return msgs
	}

	base := func() PolicyModel {
		m := wpModel()
		m.Scope = types.StringValue(scopeOverlay)
		return m
	}

	if msgs := check(wpModel()); len(msgs) != 0 {
		t.Errorf("a plain gateway policy was refused: %v", msgs)
	}
	if msgs := check(base()); len(msgs) != 0 {
		t.Errorf("a plain overlay policy was refused: %v", msgs)
	}

	t.Run("inherit needs an overlay", func(t *testing.T) {
		ok := base()
		ok.Mode = types.StringValue(modeInherit)
		if msgs := check(ok); len(msgs) != 0 {
			t.Errorf("inherit on an overlay was refused: %v", msgs)
		}

		bad := wpModel() // scope null -> gateway, which is the server's default
		bad.Mode = types.StringValue(modeInherit)
		msgs := check(bad)
		if len(msgs) == 0 {
			t.Fatal("mode = inherit with no scope was accepted; an unset scope is a GATEWAY policy")
		}
		if !strings.Contains(strings.Join(msgs, " "), "nothing to inherit from") {
			t.Errorf("the refusal does not say why inherit cannot apply here: %v", msgs)
		}

		explicit := wpModel()
		explicit.Scope = types.StringValue(scopeGateway)
		explicit.Mode = types.StringValue(modeInherit)
		if msgs := check(explicit); len(msgs) == 0 {
			t.Error("mode = inherit with scope = gateway was accepted")
		}
	})

	t.Run("the CRS dials belong to a gateway policy", func(t *testing.T) {
		for _, tc := range []struct {
			attr string
			set  func(*PolicyModel)
		}{
			{"paranoia_level", func(m *PolicyModel) { m.ParanoiaLevel = types.Int64Value(3) }},
			{"anomaly_score_threshold", func(m *PolicyModel) { m.AnomalyScoreThreshold = types.Int64Value(5) }},
			{"managed_ruleset_version", func(m *PolicyModel) { m.CRSVersion = types.StringValue("4.7.0") }},
		} {
			t.Run(tc.attr, func(t *testing.T) {
				overlay := base()
				tc.set(&overlay)
				msgs := check(overlay)
				if len(msgs) == 0 {
					t.Fatalf("an overlay accepted %s, which it is not compiled with", tc.attr)
				}
				if !strings.Contains(strings.Join(msgs, " "), "WITHOUT the managed ruleset") {
					t.Errorf("the refusal does not say why: %v", msgs)
				}

				// The same dial on a GATEWAY policy is exactly right, so the
				// guard is keyed on scope and not simply refusing the attribute.
				gw := wpModel()
				tc.set(&gw)
				if msgs := check(gw); len(msgs) != 0 {
					t.Errorf("a gateway policy owns %s: %v", tc.attr, msgs)
				}
			})
		}
	})
}

// TestRequestBodyLimitValidatorUsesTheFrameBounds.
//
// The bounds are 4096-40960 now: 40960 is the inspection engine's frame cap,
// not a memory budget. 131072 was the OLD floor this provider advertised, so it
// is the value most likely to be sitting in a practitioner's HCL -- and it is
// now a 400. The validator is exercised through the SCHEMA, because that is
// what a plan runs; asserting on a constant would pass with the validator
// deleted.
func TestRequestBodyLimitValidatorUsesTheFrameBounds(t *testing.T) {
	if minRequestBodyLimitBytes != 4096 || maxRequestBodyLimitBytes != 40960 {
		t.Fatalf("bounds are %d-%d, want 4096-40960", minRequestBodyLimitBytes, maxRequestBodyLimitBytes)
	}

	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attr, ok := sr.Schema.Attributes["request_body_limit_bytes"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("request_body_limit_bytes is not an Int64Attribute")
	}
	if len(attr.Validators) == 0 {
		t.Fatal("request_body_limit_bytes has no validator, so any value reaches the server")
	}

	run := func(n int64) bool {
		var resp validator.Int64Response
		for _, v := range attr.Validators {
			v.ValidateInt64(context.Background(), validator.Int64Request{
				ConfigValue: types.Int64Value(n),
			}, &resp)
		}
		return resp.Diagnostics.HasError()
	}
	for _, n := range []int64{4096, 8192, 40960} {
		if run(n) {
			t.Errorf("%d is inside the accepted range but was refused", n)
		}
	}
	for _, n := range []int64{1024, 4095, 40961, 131072, 1 << 20, 536870912} {
		if !run(n) {
			t.Errorf("%d was accepted; the server refuses it", n)
		}
	}
}

// TestModeValidatorAcceptsInherit. The vocabulary is three values now, and a
// validator still pinned to two refuses a legal overlay configuration at plan
// time -- with a message about the provider, not the platform.
func TestModeValidatorAcceptsInherit(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attr, ok := sr.Schema.Attributes["mode"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("mode is not a StringAttribute")
	}
	run := func(s string) bool {
		var resp validator.StringResponse
		for _, v := range attr.Validators {
			v.ValidateString(context.Background(), validator.StringRequest{
				ConfigValue: types.StringValue(s),
			}, &resp)
		}
		return resp.Diagnostics.HasError()
	}
	for _, m := range []string{modeDetect, modeBlock, modeInherit} {
		if run(m) {
			t.Errorf("mode = %q was refused by the schema validator", m)
		}
	}
	if !run("monitor") {
		t.Error("an unknown mode was accepted")
	}
}

// TestEffectiveModeIsComputedOnly. A practitioner cannot set what the engine
// resolves; making it Optional would let a configuration assert a mode the
// server never agreed to and then show no diff when reality differed.
func TestEffectiveModeIsComputedOnly(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attr, ok := sr.Schema.Attributes["effective_mode"]
	if !ok {
		t.Fatal("the schema has no effective_mode attribute, so nothing reports what is in force")
	}
	if !attr.IsComputed() || attr.IsOptional() || attr.IsRequired() {
		t.Fatalf("effective_mode must be Computed-only, got computed=%v optional=%v required=%v",
			attr.IsComputed(), attr.IsOptional(), attr.IsRequired())
	}
}

func warningsMention(ws diag.Diagnostics, want string) bool {
	for _, w := range ws {
		if strings.Contains(w.Detail(), want) {
			return true
		}
	}
	return false
}

// TestEffectiveModeIsNeverTheInheritToken states the rule on its own so it
// cannot be weakened one call site at a time.
//
// The attribute's documented contract is "`mode` with `inherit` resolved", and
// the resource docs promise that an inheriting overlay under a blocking gateway
// has effective_mode = "block". A fallback that can put "inherit" in there
// breaks both promises silently: a practitioner writing
// `self.effective_mode == "block" ? 1 : 0` gets a wrong answer rather than a
// visibly absent one.
func TestEffectiveModeIsNeverTheInheritToken(t *testing.T) {
	for _, mode := range []string{modeDetect, modeBlock, modeInherit, ""} {
		for _, computed := range []string{"", modeDetect, modeBlock} {
			if got := effectiveMode(mode, computed); got == modeInherit {
				t.Errorf("effectiveMode(%q, %q) = %q", mode, computed, got)
			}
			if got := effectiveModeValue(mode, computed); got.ValueString() == modeInherit {
				t.Errorf("effectiveModeValue(%q, %q) = %v", mode, computed, got)
			}
		}
	}
	// Null, not the empty string: null is what try()/coalesce() handle and what
	// cannot be mistaken for a mode.
	if v := effectiveModeValue(modeInherit, ""); !v.IsNull() {
		t.Fatalf("an unresolved inherit rendered as %v, want null", v)
	}
}
