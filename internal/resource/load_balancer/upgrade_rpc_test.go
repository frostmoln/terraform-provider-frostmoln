package load_balancer_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/load_balancer"
)

// These drive the real UpgradeResourceState and PlanResourceChange RPCs -- what
// `terraform plan` runs after the provider bump -- against LITERAL prior state
// rows, to show that retiring flavor_id costs an existing load balancer nothing:
// the stored row upgrades, and the plan that follows against an unchanged
// configuration is a no-op with no replacement.

const lbTypeName = "frostmoln_load_balancer"

// v1Row is a version-1 state row. flavor_id is null, as it is in every real
// one: the platform has no load balancer flavors, so no create that set it ever
// succeeded.
const v1Row = `{
  "id": "lb-1", "name": "web", "vpc_id": "vpc-1", "subnet_id": "subnet-1",
  "description": null, "vip_address": "10.0.0.5", "scheme": "internal",
  "public_ip_id": null, "public_ip_address": null, "type": "l7",
  "flavor_id": null,
  "tags": {"env": "prod"}, "tags_all": {"env": "prod"},
  "vip_port_id": "port-1", "status": "active", "provisioning_status": "ACTIVE",
  "operating_status": "ONLINE", "created_at": "2026-01-01T00:00:00Z",
  "updated_at": null, "timeouts": null
}`

// v0Row is a version-0 state row: provider_type instead of type, no tags_all,
// no timeouts block, and flavor_id null.
const v0Row = `{
  "id": "lb-1", "name": "web", "vpc_id": "vpc-1", "subnet_id": "subnet-1",
  "description": null, "vip_address": "10.0.0.5", "scheme": "internal",
  "public_ip_id": null, "public_ip_address": null, "provider_type": "amphora",
  "flavor_id": null,
  "tags": {"env": "prod"},
  "vip_port_id": "port-1", "status": "active", "provisioning_status": "ACTIVE",
  "operating_status": "ONLINE", "created_at": "2026-01-01T00:00:00Z",
  "updated_at": null
}`

func lbSchema(t *testing.T) (tftypes.Object, map[string]bool) {
	t.Helper()
	var sr fwresource.SchemaResponse
	load_balancer.NewResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	obj, ok := sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema is not an object")
	}
	computed := map[string]bool{}
	for name, a := range sr.Schema.Attributes {
		computed[name] = a.IsComputed()
	}
	return obj, computed
}

func lbDynamic(t *testing.T, obj tftypes.Object, v tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(obj, v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return &dv
}

func failOnErrors(t *testing.T, what string, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("%s: %s -- %s", what, d.Summary, d.Detail)
		}
	}
}

// upgradeThenPlan upgrades a stored row of the given version through the RPC,
// then plans the configuration a customer of that load balancer holds today --
// which no longer names flavor_id -- against the upgraded state.
func upgradeThenPlan(t *testing.T, version int64, row string) {
	t.Helper()
	ctx := context.Background()
	obj, computed := lbSchema(t)
	server := providerserver.NewProtocol6(provider.New("test")())()

	up, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: lbTypeName,
		Version:  version,
		RawState: &tfprotov6.RawState{JSON: []byte(row)},
	})
	if err != nil {
		t.Fatalf("UpgradeResourceState: %v", err)
	}
	failOnErrors(t, "upgrade", up.Diagnostics)
	prior, err := up.UpgradedState.Unmarshal(obj)
	if err != nil {
		t.Fatalf("the upgraded state does not decode against the current schema: %v", err)
	}
	var priorAttrs map[string]tftypes.Value
	if err := prior.As(&priorAttrs); err != nil {
		t.Fatalf("upgraded state is not an object: %v", err)
	}
	if _, ok := priorAttrs["flavor_id"]; ok {
		t.Fatal("flavor_id survived the upgrade")
	}

	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
	cfgAttrs := map[string]tftypes.Value{}
	for name, at := range obj.AttributeTypes {
		cfgAttrs[name] = tftypes.NewValue(at, nil)
	}
	cfgAttrs["name"] = str("web")
	cfgAttrs["vpc_id"] = str("vpc-1")
	cfgAttrs["subnet_id"] = str("subnet-1")
	cfgAttrs["type"] = str("l7")
	cfgAttrs["tags"] = tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{"env": str("prod")})
	config := tftypes.NewValue(obj, cfgAttrs)

	// The proposed new state as Terraform core builds it: the configuration,
	// with a Computed attribute the configuration leaves null taking its prior
	// value.
	propAttrs := map[string]tftypes.Value{}
	for name, v := range cfgAttrs {
		if v.IsNull() && computed[name] {
			propAttrs[name] = priorAttrs[name]
		} else {
			propAttrs[name] = v
		}
	}
	proposed := tftypes.NewValue(obj, propAttrs)

	plan, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         lbTypeName,
		PriorState:       lbDynamic(t, obj, prior),
		ProposedNewState: lbDynamic(t, obj, proposed),
		Config:           lbDynamic(t, obj, config),
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	failOnErrors(t, "plan", plan.Diagnostics)
	if len(plan.RequiresReplace) != 0 {
		t.Fatalf("the first plan after the upgrade REPLACES the load balancer (paths %v): a new VIP and an outage", plan.RequiresReplace)
	}
	planned, err := plan.PlannedState.Unmarshal(obj)
	if err != nil {
		t.Fatalf("decode planned state: %v", err)
	}
	if !planned.Equal(prior) {
		diffs, _ := planned.Diff(prior)
		t.Fatalf("the first plan after the upgrade is not a no-op: %v", diffs)
	}
}

// Versions here are WIRE versions: idform.Wrap (provider.go) adds one to the
// resource's own schema Version. The release before this change stored the
// resource's own v1 as wire 2, so wire 2 is the row nearly every existing
// state holds. It reaches idform's id-rewrite-only upgrader, which decodes
// against the CURRENT schema with undefined attributes ignored -- that is
// what drops flavor_id on this path, and this case is what pins it.
func TestUpgradeFromReleasedWireV2WithNullFlavorPlansNoChange(t *testing.T) {
	upgradeThenPlan(t, 2, v1Row)
}

// Wire 1 is the resource's own v1 written before the idform bump existed; it
// runs this package's v1 upgrader (dropRetiredFlavorID) and then the id rewrite.
func TestUpgradeFromV1WithNullFlavorPlansNoChange(t *testing.T) {
	upgradeThenPlan(t, 1, v1Row)
}

// Wire 0 is the resource's own v0 (provider_type), which upgrades straight to
// the current schema through this package's v0 upgrader.
func TestUpgradeFromV0WithNullFlavorPlansNoChange(t *testing.T) {
	upgradeThenPlan(t, 0, v0Row)
}
