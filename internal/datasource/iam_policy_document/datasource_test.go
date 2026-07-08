package iam_policy_document

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// --- pure composer tests (the load-bearing logic) ---

func TestBuildPolicyJSON_ScalarAndArrayConstraints(t *testing.T) {
	got, err := buildPolicyJSON([]ruleInput{
		{
			Name:       "compute-read-in-region",
			Access:     "allow",
			Operations: []string{"compute:instances:read", "compute:instances:list"},
			Targets:    []string{"frn:compute:se-sto-1:*:instances/*"},
			Constraints: []constraintInput{
				// scalar operator -> emitted as a bare string
				{Operator: "equals", Key: "frn:region", Values: []string{"se-sto-1"}},
				// ipInRange -> emitted as a JSON array (even multi-value)
				{Operator: "ipInRange", Key: "frn:sourceIp", Values: []string{"203.0.113.0/24", "198.51.100.0/24"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Decode and assert the value shapes match the frozen contract exactly.
	var doc struct {
		SchemaVersion string `json:"schemaVersion"`
		Rules         []struct {
			Name        string                    `json:"name"`
			Access      string                    `json:"access"`
			Operations  []string                  `json:"operations"`
			Targets     []string                  `json:"targets"`
			Constraints map[string]map[string]any `json:"constraints"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, got)
	}
	if doc.SchemaVersion != "1" {
		t.Errorf("schemaVersion = %q, want 1", doc.SchemaVersion)
	}
	if len(doc.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(doc.Rules))
	}
	r := doc.Rules[0]
	if r.Access != "allow" || r.Name != "compute-read-in-region" {
		t.Errorf("unexpected rule head: %+v", r)
	}
	// equals value must be a scalar string, not an array.
	if v, ok := r.Constraints["equals"]["frn:region"].(string); !ok || v != "se-sto-1" {
		t.Errorf("equals/frn:region = %#v, want scalar string \"se-sto-1\"", r.Constraints["equals"]["frn:region"])
	}
	// ipInRange value must be an array.
	arr, ok := r.Constraints["ipInRange"]["frn:sourceIp"].([]any)
	if !ok || len(arr) != 2 {
		t.Errorf("ipInRange/frn:sourceIp = %#v, want 2-element array", r.Constraints["ipInRange"]["frn:sourceIp"])
	}
}

func TestBuildPolicyJSON_ScalarOperatorRejectsMultipleValues(t *testing.T) {
	_, err := buildPolicyJSON([]ruleInput{{
		Access:     "deny",
		Operations: []string{"compute:*:delete"},
		Targets:    []string{"*"},
		Constraints: []constraintInput{
			{Operator: "equals", Key: "frn:region", Values: []string{"se-sto-1", "se-sto-2"}},
		},
	}})
	if err == nil {
		t.Fatal("expected an error: a scalar operator cannot take multiple values")
	}
}

func TestBuildPolicyJSON_DuplicateConstraintRejected(t *testing.T) {
	_, err := buildPolicyJSON([]ruleInput{{
		Access:     "allow",
		Operations: []string{"compute:*"},
		Targets:    []string{"*"},
		Constraints: []constraintInput{
			{Operator: "equals", Key: "frn:region", Values: []string{"a"}},
			{Operator: "equals", Key: "frn:region", Values: []string{"b"}},
		},
	}})
	if err == nil {
		t.Fatal("expected an error on duplicate operator/key constraint")
	}
}

func TestBuildPolicyJSON_NoConstraintsOmitsKeyAndEmptyRulesIsArray(t *testing.T) {
	got, err := buildPolicyJSON([]ruleInput{{
		Access:     "allow",
		Operations: []string{"compute:instances:read"},
		Targets:    []string{"*"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// constraints key must be absent when there are none (omitempty).
	if want := `"constraints"`; contains(got, want) {
		t.Errorf("expected no constraints key, got %s", got)
	}

	// A zero-rule document must serialize rules as [] (never null).
	empty, err := buildPolicyJSON(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(empty, `"rules":[]`) {
		t.Errorf("expected rules:[] for an empty document, got %s", empty)
	}
}

func TestBuildPolicyJSON_UnnamedRuleEmitsEmptyName(t *testing.T) {
	// The server canonicalizes a stored policy by re-marshalling through
	// authz.AccessPolicy, whose Rule.Name is `json:"name"` (no omitempty). An
	// unnamed rule must therefore emit `"name":""` here too, or a
	// frostmoln_iam_policy using it would fail "inconsistent result after apply".
	got, err := buildPolicyJSON([]ruleInput{{
		Access:     "allow",
		Operations: []string{"compute:instances:read"},
		Targets:    []string{"*"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(got, `"name":""`) {
		t.Errorf("expected an unnamed rule to serialize \"name\":\"\" (congruent with authz.Rule), got %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- data source plumbing tests ---

func TestDocumentDataSource_Metadata(t *testing.T) {
	d := &documentDataSource{}
	resp := datasource.MetadataResponse{}
	d.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_iam_policy_document" {
		t.Errorf("type name = %q", resp.TypeName)
	}
}

func documentSchema(t *testing.T) schema.Schema {
	t.Helper()
	d := &documentDataSource{}
	resp := datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func TestDocumentDataSource_Schema(t *testing.T) {
	s := documentSchema(t)
	if _, ok := s.Attributes["json"]; !ok {
		t.Error("expected a json attribute")
	}
	if _, ok := s.Blocks["rule"]; !ok {
		t.Error("expected a rule block")
	}
}

// TestDocumentDataSource_Read exercises the full config->json plumbing (nested
// blocks, ElementsAs, id/json output) with one rule and one constraint.
func TestDocumentDataSource_Read(t *testing.T) {
	ctx := context.Background()
	d := &documentDataSource{}
	s := documentSchema(t)
	tfType := s.Type().TerraformType(ctx)

	strList := func(vals ...string) tftypes.Value {
		elems := make([]tftypes.Value, len(vals))
		for i, v := range vals {
			elems[i] = tftypes.NewValue(tftypes.String, v)
		}
		return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
	}

	constraintObjType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"operator": tftypes.String,
		"key":      tftypes.String,
		"values":   tftypes.List{ElementType: tftypes.String},
	}}
	ruleObjType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"name":       tftypes.String,
		"access":     tftypes.String,
		"operations": tftypes.List{ElementType: tftypes.String},
		"targets":    tftypes.List{ElementType: tftypes.String},
		"constraint": tftypes.List{ElementType: constraintObjType},
	}}

	constraint := tftypes.NewValue(constraintObjType, map[string]tftypes.Value{
		"operator": tftypes.NewValue(tftypes.String, "equals"),
		"key":      tftypes.NewValue(tftypes.String, "frn:region"),
		"values":   strList("se-sto-1"),
	})
	rule := tftypes.NewValue(ruleObjType, map[string]tftypes.Value{
		"name":       tftypes.NewValue(tftypes.String, "r1"),
		"access":     tftypes.NewValue(tftypes.String, "allow"),
		"operations": strList("compute:instances:read"),
		"targets":    strList("frn:compute:se-sto-1:*:instances/*"),
		"constraint": tftypes.NewValue(tftypes.List{ElementType: constraintObjType}, []tftypes.Value{constraint}),
	})

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"json": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"rule": tftypes.NewValue(tftypes.List{ElementType: ruleObjType}, []tftypes.Value{rule}),
	})

	readResp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: configVal}}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", readResp.Diagnostics.Errors())
	}

	var out documentModel
	readResp.State.Get(ctx, &out)
	if out.JSON.IsNull() || out.JSON.ValueString() == "" {
		t.Fatal("expected a json output")
	}
	if out.ID.IsNull() || out.ID.ValueString() == "" {
		t.Fatal("expected an id output")
	}
	if !contains(out.JSON.ValueString(), `"frn:region":"se-sto-1"`) {
		t.Errorf("expected the equals scalar constraint in output, got %s", out.JSON.ValueString())
	}
}
