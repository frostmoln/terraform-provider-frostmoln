// Public-id switch acceptance gates (ADR-0129, PUBLIC-IDS-PLAN D6 step 3).
//
// This build asks the gateway for public ids (Frostmoln-Id-Format: public) and
// wraps every resource in internal/idform. Releases up to v0.87.x asked for
// UUIDs. These gates prove a practitioner can take the upgrade with no
// replacement and, once applied, no lingering diff:
//
//	(a) TestAccPublicID_UpgradeFromRelease: apply with the released provider
//	    (registry frostmoln/frostmoln, ">= 0.87.5, < 0.88.0"), switch to this
//	    build, plan EMPTY. Covers vpc, subnet (vpc_id), security_group
//	    (vpc_id), two security_group_rules (one with remote_group_id),
//	    vpc_route (composite id vpcID/destinationCIDR) and launch_template
//	    (the security_group_ids set).
//	(b) TestAccPublicID_HardcodedUUID: HCL that hardcodes UUIDs (subnet and
//	    launch_template vpc_id, rule security_group_id/remote_group_id,
//	    launch_template security_group_ids) plans no replacement under this
//	    build, and the plan after the apply is empty.
//	(c) TestAccPublicID_Import: import by UUID and by public id, including the
//	    composite security_group_rule id (<sg>/<rule>, all four spelling mixes)
//	    and vpc_route (<vpc>/<cidr>), with `terraform import` and with import
//	    blocks that must plan no replacement and converge.
//	(d) TestAccPublicID_DataSourceVPC: a subnet vpc_id fed from
//	    data "frostmoln_vpc" (looked up by name, and by id = "<uuid>") plans
//	    no replacement: across the upgrade, and when a hardcoded UUID and the
//	    two data sources' ids are swapped for one another.
//
// Run: TF_ACC=1 go test ./internal/acctest/ -run PublicID -count=1 -v
// (terraform on PATH; gate (a), (b) and (d) download the released provider
// from registry.terraform.io, so they need network).
//
// By default every gate runs against fakegw, an in-process fake gateway that
// keys entities by UUID, accepts either id form on input and renders public
// ids exactly when the request asks for them. FROSTMOLN_* variables are
// cleared for the run so nothing reaches a real tenant.
//
// LIVE MODE: FROSTMOLN_IDFORM_GATE_LIVE=1 skips the fake and runs the same
// gates against the tenant behind FROSTMOLN_API_ENDPOINT / FROSTMOLN_API_KEY
// (both required, read from the environment by both providers). It creates
// real, if cheap, network objects (VPCs, subnets, security groups, a VPC
// route, a launch template) and destroys them at the end. Live mode also reads
// FROSTMOLN_TEST_IMAGE_NAME (default "Ubuntu 24.04") and
// FROSTMOLN_TEST_FLAVOR_ID (default "gp1.small") for the launch template.
package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest/fakegw"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
)

// releasedProvider is the provider before the switch: it asks for UUIDs.
var releasedProvider = map[string]resource.ExternalProvider{
	"frostmoln": {Source: "frostmoln/frostmoln", VersionConstraint: ">= 0.87.5, < 0.88.0"},
}

// gate is one gate's environment: the fake (nil in live mode) and a name
// prefix for what it creates.
type gate struct {
	t    *testing.T
	fake *fakegw.Server
	name string
}

func newGate(t *testing.T) *gate {
	t.Helper()
	// The in-process build registers as registry.terraform.io/frostmoln/frostmoln,
	// the released provider's address, so state written by one is state the
	// other owns.
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "frostmoln")
	g := &gate{t: t, name: RandomName("pid")}
	if os.Getenv("FROSTMOLN_IDFORM_GATE_LIVE") == "1" {
		TestAccPreCheck(t)
		return g
	}
	for _, v := range []string{"FROSTMOLN_API_ENDPOINT", "FROSTMOLN_API_KEY", "FROSTMOLN_TENANT_ID", "FROSTMOLN_CLI_CONFIG"} {
		t.Setenv(v, "")
	}
	t.Setenv("FROSTMOLN_USE_CLI_CONFIG", "false")
	g.fake = fakegw.New()
	t.Cleanup(func() {
		t.Logf("fakegw user agents: %v", g.fake.UserAgents())
		if u := g.fake.Unrouted(); len(u) > 0 {
			t.Errorf("fakegw had no route for: %v", u)
		}
		g.fake.Close()
	})
	return g
}

// provider is the provider block both builds read.
func (g *gate) provider() string {
	if g.fake == nil {
		return "provider \"frostmoln\" {\n  use_cli_config = false\n}\n"
	}
	return fmt.Sprintf("provider \"frostmoln\" {\n  api_endpoint   = %q\n  api_key        = \"fakegw-key\"\n  use_cli_config = false\n}\n", g.fake.Endpoint())
}

// checkDestroy asserts, against the fake, that destroy removed everything.
func (g *gate) checkDestroy(*terraform.State) error {
	if g.fake == nil {
		return nil
	}
	if left := g.fake.Remaining(); len(left) > 0 {
		return fmt.Errorf("fakegw still holds after destroy: %v", left)
	}
	return nil
}

// countRequests counts fake requests whose "METHOD path" matches re.
func (g *gate) countRequests(re string) int {
	if g.fake == nil {
		return -1
	}
	rx := regexp.MustCompile(re)
	n := 0
	for _, r := range g.fake.Requests() {
		if rx.MatchString(r) {
			n++
		}
	}
	return n
}

func imageName() string { return TestImageName() }

func flavorID() string {
	if v := os.Getenv("FROSTMOLN_TEST_FLAVOR_ID"); v != "" {
		return v
	}
	return "gp1.small"
}

// id is one id captured from state in both spellings.
type id struct {
	uuid, pub string
}

// lazy is a Terraform input variable read when the step runs, so a later step
// can hardcode an id an earlier step created.
type lazy func() string

func (f lazy) MarshalJSON() ([]byte, error) { return json.Marshal(f()) }

func (i *id) uuidVar() config.Variable { return lazy(func() string { return i.uuid }) }

// capture records attr of addr into dst in both spellings; prefix is the kind
// a UUID is encoded with.
func capture(addr, attr, prefix string, dst *id) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		v := rs.Primary.Attributes[attr]
		u, p, ok := pubid.Canonical(v)
		if !ok {
			return fmt.Errorf("%s.%s = %q is not an id", addr, attr, v)
		}
		if p != "" && pubid.Kind(p) != pubid.Kind(prefix) {
			return fmt.Errorf("%s.%s = %q has prefix %q, want %q", addr, attr, v, p, prefix)
		}
		pub, ok := pubid.Encode(prefix, u)
		if !ok {
			return fmt.Errorf("cannot encode %q as %q", u, prefix)
		}
		dst.uuid, dst.pub = u, pub
		return nil
	}
}

// snapshot copies addr's flat state attributes into dst.
func snapshot(addr string, dst map[string]string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		for k, v := range rs.Primary.Attributes {
			dst[k] = v
		}
		return nil
	}
}

var (
	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	pubRe  = func(prefix string) *regexp.Regexp {
		return regexp.MustCompile(`^` + prefix + `_[1-9A-HJ-NP-Za-km-z]{22}$`)
	}
)

// sameSpelling reports whether a and b are equal, or the same ids in two
// spellings, part by part on "/".
func sameSpelling(a, b string) bool {
	if a == b {
		return true
	}
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	if len(pa) != len(pb) {
		return false
	}
	for i := range pa {
		if pa[i] == pb[i] {
			continue
		}
		ua, _, oka := pubid.Canonical(pa[i])
		ub, _, okb := pubid.Canonical(pb[i])
		if !oka || !okb || ua != ub {
			return false
		}
	}
	return true
}

// importMatches checks the one imported instance holds want's attributes, up
// to id spelling. timeouts are config-only and never imported.
func importMatches(want map[string]string) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		if len(states) != 1 {
			return fmt.Errorf("imported %d instances, want 1", len(states))
		}
		got := states[0].Attributes
		var diffs []string
		for k, w := range want {
			if strings.HasPrefix(k, "timeouts") {
				continue
			}
			if g := got[k]; !sameSpelling(g, w) {
				diffs = append(diffs, fmt.Sprintf("%s: imported %q, created %q", k, g, w))
			}
		}
		for k, g := range got {
			if _, ok := want[k]; !ok && !strings.HasPrefix(k, "timeouts") {
				diffs = append(diffs, fmt.Sprintf("%s: imported %q, absent at create", k, g))
			}
		}
		if len(diffs) > 0 {
			sort.Strings(diffs)
			return fmt.Errorf("import differs:\n  %s", strings.Join(diffs, "\n  "))
		}
		return nil
	}
}

// noReplace is a plan check: addr is in the plan as a no-op or an in-place
// update, never a replacement, create or delete.
type noReplace struct{ addr string }

func expectNoReplace(addr string) plancheck.PlanCheck { return noReplace{addr} }

func (c noReplace) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != c.addr {
			continue
		}
		a := rc.Change.Actions
		if a.NoOp() || a.Update() {
			return
		}
		resp.Error = fmt.Errorf("%s: planned %v (replace paths %v), want no-op or update", c.addr, a, rc.Change.ReplacePaths)
		return
	}
	resp.Error = fmt.Errorf("%s: not in the plan", c.addr)
}

// importing is a plan check: addr is planned as an import.
type importing struct{ addr string }

func (c importing) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address == c.addr {
			if rc.Change.Importing == nil {
				resp.Error = fmt.Errorf("%s: planned %v without importing", c.addr, rc.Change.Actions)
			}
			return
		}
	}
	resp.Error = fmt.Errorf("%s: not in the plan", c.addr)
}

// --- (a) upgrade from the released provider --------------------------------

func (g *gate) configUpgrade() string {
	return g.provider() + fmt.Sprintf(`
data "frostmoln_image" "img" {
  name = %[2]q
}

resource "frostmoln_vpc" "v" {
  name = "%[1]s-vpc"
  cidr = "10.50.0.0/16"
}

resource "frostmoln_subnet" "s" {
  name   = "%[1]s-sub"
  cidr   = "10.50.1.0/24"
  vpc_id = frostmoln_vpc.v.id
}

resource "frostmoln_security_group" "g" {
  name   = "%[1]s-sg"
  vpc_id = frostmoln_vpc.v.id
}

resource "frostmoln_security_group_rule" "cidr" {
  security_group_id = frostmoln_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_cidr       = "10.50.0.0/16"
}

resource "frostmoln_security_group_rule" "self" {
  security_group_id = frostmoln_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_group_id   = frostmoln_security_group.g.id
}

resource "frostmoln_vpc_route" "r" {
  vpc_id      = frostmoln_vpc.v.id
  destination = "192.168.50.0/24"
  next_hop    = "10.50.1.10"
  depends_on  = [frostmoln_subnet.s]
}

resource "frostmoln_launch_template" "lt" {
  name               = "%[1]s-lt"
  flavor_id          = %[3]q
  image_id           = data.frostmoln_image.img.id
  vpc_id             = frostmoln_vpc.v.id
  security_group_ids = [frostmoln_security_group.g.id]
}
`, g.name, imageName(), flavorID())
}

func TestAccPublicID_UpgradeFromRelease(t *testing.T) {
	g := newGate(t)
	cfg := g.configUpgrade()
	resource.Test(t, resource.TestCase{
		CheckDestroy: g.checkDestroy,
		Steps: []resource.TestStep{
			{
				ExternalProviders: releasedProvider,
				Config:            cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					// The released provider asked for, and stored, UUIDs.
					resource.TestMatchResourceAttr("frostmoln_vpc.v", "id", uuidRe),
					resource.TestMatchResourceAttr("frostmoln_subnet.s", "vpc_id", uuidRe),
					resource.TestMatchResourceAttr("frostmoln_security_group_rule.self", "remote_group_id", uuidRe),
					resource.TestMatchResourceAttr("frostmoln_launch_template.lt", "security_group_ids.0", uuidRe),
				),
			},
			{
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// The state upgrader rewrote every listed id to its public form.
					resource.TestMatchResourceAttr("frostmoln_vpc.v", "id", pubRe("vpc")),
					resource.TestMatchResourceAttr("frostmoln_subnet.s", "id", pubRe("sub")),
					resource.TestMatchResourceAttr("frostmoln_subnet.s", "vpc_id", pubRe("vpc")),
					resource.TestMatchResourceAttr("frostmoln_security_group.g", "vpc_id", pubRe("vpc")),
					resource.TestMatchResourceAttr("frostmoln_security_group_rule.cidr", "id", pubRe("sgr")),
					resource.TestMatchResourceAttr("frostmoln_security_group_rule.cidr", "security_group_id", pubRe("sg")),
					resource.TestMatchResourceAttr("frostmoln_security_group_rule.self", "remote_group_id", pubRe("sg")),
					resource.TestMatchResourceAttr("frostmoln_vpc_route.r", "vpc_id", pubRe("vpc")),
					resource.TestMatchResourceAttr("frostmoln_vpc_route.r", "id",
						regexp.MustCompile(`^vpc_[1-9A-HJ-NP-Za-km-z]{22}/192\.168\.50\.0/24$`)),
					resource.TestMatchResourceAttr("frostmoln_launch_template.lt", "id", pubRe("lt")),
					resource.TestMatchResourceAttr("frostmoln_launch_template.lt", "vpc_id", pubRe("vpc")),
					resource.TestMatchResourceAttr("frostmoln_launch_template.lt", "image_id", pubRe("img")),
					resource.TestMatchResourceAttr("frostmoln_launch_template.lt", "security_group_ids.0", pubRe("sg")),
				),
			},
		},
	})
}

// --- (b) hardcoded UUIDs -----------------------------------------------------

func (g *gate) configHardcodedBase() string {
	return g.provider() + fmt.Sprintf(`
resource "frostmoln_vpc" "v" {
  name = "%[1]s-vpc"
  cidr = "10.51.0.0/16"
}

resource "frostmoln_security_group" "g" {
  name   = "%[1]s-sg"
  vpc_id = frostmoln_vpc.v.id
}
`, g.name)
}

func (g *gate) configHardcoded() string {
	return g.configHardcodedBase() + fmt.Sprintf(`
variable "vpc_uuid" {
  type = string
}

variable "sg_uuid" {
  type = string
}

data "frostmoln_image" "img" {
  name = %[2]q
}

resource "frostmoln_subnet" "s" {
  name   = "%[1]s-sub"
  cidr   = "10.51.1.0/24"
  vpc_id = var.vpc_uuid
}

resource "frostmoln_security_group_rule" "self" {
  security_group_id = var.sg_uuid
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_group_id   = var.sg_uuid
}

resource "frostmoln_launch_template" "lt" {
  name               = "%[1]s-lt"
  flavor_id          = %[3]q
  image_id           = data.frostmoln_image.img.id
  vpc_id             = var.vpc_uuid
  security_group_ids = [var.sg_uuid]
}
`, g.name, imageName(), flavorID())
}

func TestAccPublicID_HardcodedUUID(t *testing.T) {
	g := newGate(t)
	var vpc, sg id
	vars := config.Variables{"vpc_uuid": vpc.uuidVar(), "sg_uuid": sg.uuidVar()}
	hardcoded := []string{"frostmoln_subnet.s", "frostmoln_security_group_rule.self", "frostmoln_launch_template.lt"}
	noReplaceAll := make([]plancheck.PlanCheck, 0, len(hardcoded))
	for _, a := range hardcoded {
		noReplaceAll = append(noReplaceAll, expectNoReplace(a))
	}
	var ruleWrites int
	resource.Test(t, resource.TestCase{
		CheckDestroy: g.checkDestroy,
		Steps: []resource.TestStep{
			{
				ExternalProviders: releasedProvider,
				Config:            g.configHardcodedBase(),
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("frostmoln_vpc.v", "id", "vpc", &vpc),
					capture("frostmoln_security_group.g", "id", "sg", &sg),
					resource.TestMatchResourceAttr("frostmoln_vpc.v", "id", uuidRe),
				),
			},
			{
				ExternalProviders: releasedProvider,
				Config:            g.configHardcoded(),
				ConfigVariables:   vars,
			},
			{
				// The upgrader rewrites state to public ids; the HCL still says
				// UUID. No replacement, and the apply converges.
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   g.configHardcoded(),
				ConfigVariables:          vars,
				PreConfig:                func() { ruleWrites = g.countRequests(`^(PUT|PATCH|POST|DELETE) .*/security-groups/.*/rules`) },
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             noReplaceAll,
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// The configured (UUID) spelling is what state keeps.
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr("frostmoln_subnet.s", "vpc_id", vpc.uuid)(s)
					},
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr("frostmoln_security_group_rule.self", "security_group_id", sg.uuid)(s)
					},
					func(s *terraform.State) error {
						return resource.TestCheckTypeSetElemAttr("frostmoln_launch_template.lt", "security_group_ids.*", sg.uuid)(s)
					},
					// A rule is immutable: its update must never reach the API.
					func(*terraform.State) error {
						if n := g.countRequests(`^(PUT|PATCH|POST|DELETE) .*/security-groups/.*/rules`); g.fake != nil && n != ruleWrites {
							return fmt.Errorf("the rule saw %d write(s) during the spelling-only update", n-ruleWrites)
						}
						return nil
					},
				),
			},
			{
				// The second plan is empty.
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   g.configHardcoded(),
				ConfigVariables:          vars,
				PlanOnly:                 true,
			},
		},
	})
}

// --- (c) import ---------------------------------------------------------------

const routeDest = "192.168.52.0/24"

func (g *gate) configImportParents() string {
	return g.provider() + fmt.Sprintf(`
resource "frostmoln_vpc" "v" {
  name = "%[1]s-vpc"
  cidr = "10.52.0.0/16"
}

resource "frostmoln_security_group" "g" {
  name   = "%[1]s-sg"
  vpc_id = frostmoln_vpc.v.id
}
`, g.name)
}

func (g *gate) configImportChildren() string {
	return fmt.Sprintf(`
resource "frostmoln_subnet" "s" {
  name   = "%[1]s-sub"
  cidr   = "10.52.1.0/24"
  vpc_id = frostmoln_vpc.v.id
}

resource "frostmoln_security_group_rule" "r" {
  security_group_id = frostmoln_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_cidr       = "10.52.0.0/16"
}

resource "frostmoln_vpc_route" "rt" {
  vpc_id      = frostmoln_vpc.v.id
  destination = %[2]q
  next_hop    = "10.52.1.10"
  depends_on  = [frostmoln_subnet.s]
}
`, g.name, routeDest)
}

func (g *gate) configImport() string { return g.configImportParents() + g.configImportChildren() }

// configForget drops the children from state but not from the platform.
func (g *gate) configForget() string {
	return g.configImportParents() + `
removed {
  from = frostmoln_subnet.s
  lifecycle {
    destroy = false
  }
}

removed {
  from = frostmoln_security_group_rule.r
  lifecycle {
    destroy = false
  }
}

removed {
  from = frostmoln_vpc_route.rt
  lifecycle {
    destroy = false
  }
}
`
}

// configImportBlocks re-adopts the children with import blocks whose ids come
// from variables.
func (g *gate) configImportBlocks() string {
	return g.configImport() + `
variable "subnet_import_id" {
  type = string
}

variable "rule_import_id" {
  type = string
}

variable "route_import_id" {
  type = string
}

import {
  to = frostmoln_subnet.s
  id = var.subnet_import_id
}

import {
  to = frostmoln_security_group_rule.r
  id = var.rule_import_id
}

import {
  to = frostmoln_vpc_route.rt
  id = var.route_import_id
}
`
}

func TestAccPublicID_Import(t *testing.T) {
	g := newGate(t)
	var vpc, subnet, sg, rule id
	subnetState, ruleState, routeState := map[string]string{}, map[string]string{}, map[string]string{}

	importCmd := func(addr, label string, idf func() string, want map[string]string) resource.TestStep {
		// No Config: an import step reuses the prior step's MERGED config;
		// its own Config would be used raw, without required_providers.
		return resource.TestStep{
			ResourceName: addr,
			ImportState:  true,
			ImportStateIdFunc: func(*terraform.State) (string, error) {
				v := idf()
				t.Logf("import %s by %s: %s", addr, label, v)
				return v, nil
			},
			ImportStateCheck: importMatches(want),
		}
	}
	join := func(a, b *string) func() string { return func() string { return *a + "/" + *b } }
	route := func(v *string) func() string { return func() string { return *v + "/" + routeDest } }

	importBlocks := func(subnetID, ruleID, routeID func() string) []resource.TestStep {
		addrs := []string{"frostmoln_subnet.s", "frostmoln_security_group_rule.r", "frostmoln_vpc_route.rt"}
		pre := []plancheck.PlanCheck{}
		for _, a := range addrs {
			pre = append(pre, expectNoReplace(a), importing{a})
		}
		return []resource.TestStep{
			{Config: g.configForget()},
			{
				Config: g.configImportBlocks(),
				ConfigVariables: config.Variables{
					"subnet_import_id": lazy(subnetID),
					"rule_import_id":   lazy(ruleID),
					"route_import_id":  lazy(routeID),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             pre,
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{Config: g.configImport(), PlanOnly: true},
		}
	}

	steps := []resource.TestStep{
		{
			Config: g.configImport(),
			Check: resource.ComposeAggregateTestCheckFunc(
				capture("frostmoln_vpc.v", "id", "vpc", &vpc),
				capture("frostmoln_subnet.s", "id", "sub", &subnet),
				capture("frostmoln_security_group.g", "id", "sg", &sg),
				capture("frostmoln_security_group_rule.r", "id", "sgr", &rule),
				snapshot("frostmoln_subnet.s", subnetState),
				snapshot("frostmoln_security_group_rule.r", ruleState),
				snapshot("frostmoln_vpc_route.rt", routeState),
			),
		},
		importCmd("frostmoln_subnet.s", "UUID", func() string { return subnet.uuid }, subnetState),
		importCmd("frostmoln_subnet.s", "public id", func() string { return subnet.pub }, subnetState),
		importCmd("frostmoln_security_group_rule.r", "uuid/uuid", join(&sg.uuid, &rule.uuid), ruleState),
		importCmd("frostmoln_security_group_rule.r", "pub/pub", join(&sg.pub, &rule.pub), ruleState),
		importCmd("frostmoln_security_group_rule.r", "uuid/pub", join(&sg.uuid, &rule.pub), ruleState),
		importCmd("frostmoln_security_group_rule.r", "pub/uuid", join(&sg.pub, &rule.uuid), ruleState),
		importCmd("frostmoln_vpc_route.rt", "uuid/cidr", route(&vpc.uuid), routeState),
		importCmd("frostmoln_vpc_route.rt", "pub/cidr", route(&vpc.pub), routeState),
	}
	steps = append(steps, importBlocks(func() string { return subnet.uuid }, join(&sg.uuid, &rule.pub), route(&vpc.uuid))...)
	steps = append(steps, importBlocks(func() string { return subnet.pub }, join(&sg.pub, &rule.uuid), route(&vpc.pub))...)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		CheckDestroy:             g.checkDestroy,
		Steps:                    steps,
	})
}

// --- (d) vpc_id from data "frostmoln_vpc" -------------------------------------

// configDataSource is gate (d)'s config. Stage 0: a subnet fed from the
// by-name data source. Stage 1 adds a subnet on the VPC's UUID literal, a
// data source looked up by that UUID (id = "<uuid>") and a subnet fed from the
// by-name one. Stage 2 swaps them: the UUID-literal subnet takes the by-name
// data source's id, the other takes the by-id data source's.
func (g *gate) configDataSource(stage int) string {
	cfg := g.provider() + fmt.Sprintf(`
resource "frostmoln_vpc" "v" {
  name = "%[1]s-vpc"
  cidr = "10.53.0.0/16"
}

data "frostmoln_vpc" "d" {
  name       = frostmoln_vpc.v.name
  depends_on = [frostmoln_vpc.v]
}

resource "frostmoln_subnet" "s1" {
  name   = "%[1]s-sub1"
  cidr   = "10.53.1.0/24"
  vpc_id = data.frostmoln_vpc.d.id
}
`, g.name)
	if stage == 0 {
		return cfg
	}
	s2, s3 := "var.vpc_uuid", "data.frostmoln_vpc.d.id"
	if stage == 2 {
		s2, s3 = "data.frostmoln_vpc.d.id", "data.frostmoln_vpc.byid.id"
	}
	return cfg + fmt.Sprintf(`
variable "vpc_uuid" {
  type = string
}

data "frostmoln_vpc" "byid" {
  id = var.vpc_uuid
}

resource "frostmoln_subnet" "s2" {
  name   = "%[1]s-sub2"
  cidr   = "10.53.2.0/24"
  vpc_id = %[2]s
}

resource "frostmoln_subnet" "s3" {
  name   = "%[1]s-sub3"
  cidr   = "10.53.3.0/24"
  vpc_id = %[3]s
}
`, g.name, s2, s3)
}

func TestAccPublicID_DataSourceVPC(t *testing.T) {
	g := newGate(t)
	var vpc id
	vars := config.Variables{"vpc_uuid": vpc.uuidVar()}
	sameVPC := func(addr, attr string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			return resource.TestCheckResourceAttrWith(addr, attr, func(v string) error {
				t.Logf("%s.%s = %s", addr, attr, v)
				if !sameSpelling(v, vpc.uuid) {
					return fmt.Errorf("%s.%s = %q is not VPC %s", addr, attr, v, vpc.uuid)
				}
				return nil
			})(s)
		}
	}
	resource.Test(t, resource.TestCase{
		CheckDestroy: g.checkDestroy,
		Steps: []resource.TestStep{
			{
				ExternalProviders: releasedProvider,
				Config:            g.configDataSource(0),
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("frostmoln_vpc.v", "id", "vpc", &vpc),
					resource.TestMatchResourceAttr("frostmoln_subnet.s1", "vpc_id", uuidRe),
				),
			},
			{
				// Across the upgrade: the data source now answers a public id and
				// the upgraded state holds the same.
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   g.configDataSource(0),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{expectNoReplace("frostmoln_subnet.s1"), plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestMatchResourceAttr("data.frostmoln_vpc.d", "id", pubRe("vpc")),
			},
			{
				// A subnet on the VPC's UUID literal keeps the UUID in state; the
				// by-id data source names the same VPC.
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   g.configDataSource(1),
				ConfigVariables:          vars,
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr("frostmoln_subnet.s2", "vpc_id", vpc.uuid)(s)
					},
					sameVPC("data.frostmoln_vpc.byid", "id"),
					sameVPC("frostmoln_subnet.s3", "vpc_id"),
				),
			},
			{
				// Swapped: s2's UUID literal for the by-name data source's public
				// id, s3's public id for the by-id data source's. The same VPC
				// either way, so no replacement, and the apply converges.
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   g.configDataSource(2),
				ConfigVariables:          vars,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						expectNoReplace("frostmoln_subnet.s1"),
						expectNoReplace("frostmoln_subnet.s2"),
						expectNoReplace("frostmoln_subnet.s3"),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr("frostmoln_subnet.s2", "vpc_id", vpc.pub)(s)
					},
					sameVPC("frostmoln_subnet.s3", "vpc_id"),
				),
			},
			{
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Config:                   g.configDataSource(2),
				ConfigVariables:          vars,
				PlanOnly:                 true,
			},
		},
	})
}
