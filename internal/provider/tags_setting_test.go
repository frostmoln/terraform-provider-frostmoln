package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/tenant_default_tags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// TestTagsIsASettingNeverMergesProviderDefaultTags is the behaviour behind the
// tagsIsASetting exemption in default_tags_contract_test.go: a resource whose
// `tags` is a tag SETTING must write exactly its `tags`, with the provider's
// default_tags NOT merged in. For frostmoln_tenant_default_tags a merge would
// turn every provider default into a tenant-wide one, stamped by the platform
// on every resource any client creates. Driven through the real protocol
// server — configure with default_tags, plan, apply — so it is the provider
// the exemption is about, not a unit in isolation.
func TestTagsIsASettingNeverMergesProviderDefaultTags(t *testing.T) {
	clearCredentialEnv(t)
	for typeName := range tagsIsASetting {
		if typeName != "frostmoln_tenant_default_tags" {
			t.Errorf("tagsIsASetting lists %s: add its behaviour case here", typeName)
		}
	}

	ctx := context.Background()
	f := tagsettingstest.New(t)
	endpoint, key, noCLI := f.Server.URL, "k", false // pragma: allowlist secret
	dt := tftypes.NewValue(defaultTagsType, map[string]tftypes.Value{
		"tags": stringMap(map[string]string{"env": "provider-default", "cost": "42"}),
	})
	srv := providerserver.NewProtocol6(New("test")())()
	cr, err := srv.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: newProviderConfig(t,
		providerConfigValues{endpoint: &endpoint, apiKey: &key, useCLIConfig: &noCLI, defaultTags: &dt})})
	if err != nil {
		t.Fatal(err)
	}
	failOnErrors(t, "configure", cr.Diagnostics)

	var sr resource.SchemaResponse
	tenant_default_tags.NewResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	objType := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if _, hasTagsAll := objType.AttributeTypes["tags_all"]; hasTagsAll {
		t.Fatal("a tag setting has no tags_all")
	}
	dyn := func(v tftypes.Value) *tfprotov6.DynamicValue {
		d, err := tfprotov6.NewDynamicValue(objType, v)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	config := func(tags map[string]string) tftypes.Value {
		return tftypes.NewValue(objType, map[string]tftypes.Value{
			"id":                          tftypes.NewValue(tftypes.String, nil),
			"tenant_id":                   tftypes.NewValue(tftypes.String, nil),
			"tags":                        stringMap(tags),
			"apply_to_existing_on_change": tftypes.NewValue(tftypes.Bool, nil),
			"timeouts":                    tftypes.NewValue(objType.AttributeTypes["timeouts"], nil),
		})
	}
	const typeName = "frostmoln_tenant_default_tags"

	prior := tftypes.NewValue(objType, nil)
	for i, tags := range []map[string]string{
		{"team": "ops", "env": "tenant"}, // create; `env` also a provider default
		{"team": "web"},                  // update
	} {
		f.Reset()
		cfg := config(tags)
		proposed := cfg
		if !prior.IsNull() {
			var pa, ca map[string]tftypes.Value
			_ = prior.As(&pa)
			_ = cfg.As(&ca)
			ca["id"], ca["tenant_id"] = pa["id"], pa["tenant_id"]
			ca["apply_to_existing_on_change"] = pa["apply_to_existing_on_change"]
			proposed = tftypes.NewValue(objType, ca)
		}
		pr, err := srv.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
			TypeName: typeName, PriorState: dyn(prior), ProposedNewState: dyn(proposed), Config: dyn(cfg),
		})
		if err != nil {
			t.Fatal(err)
		}
		failOnErrors(t, "plan", pr.Diagnostics)
		ar, err := srv.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
			TypeName: typeName, PriorState: dyn(prior), PlannedState: pr.PlannedState, Config: dyn(cfg),
			PlannedPrivate: pr.PlannedPrivate,
		})
		if err != nil {
			t.Fatal(err)
		}
		failOnErrors(t, "apply", ar.Diagnostics)

		puts := f.Only("PUT")
		if len(puts) != 1 {
			t.Fatalf("step %d: want one PUT, got %+v", i, f.Requests())
		}
		var body struct {
			Tags map[string]string `json:"tags"`
		}
		if err := json.Unmarshal(puts[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if !tftags.Equal(body.Tags, tags) {
			t.Fatalf("step %d: PUT tags = %v, want exactly the resource's tags %v — the provider's default_tags "+
				"must never be merged into a tag setting", i, body.Tags, tags)
		}
		if got := f.Defaults(f.TenantID); !tftags.Equal(got, tags) {
			t.Fatalf("step %d: the tenant's defaults are %v, want %v", i, got, tags)
		}
		if prior, err = ar.NewState.Unmarshal(objType); err != nil {
			t.Fatal(err)
		}
	}
}
