package iam_policy

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// testdata/pubid_vectors.json is a verbatim copy of servicekit
// pubid/testdata/vectors.json (servicekit 7f5864c), the vectors every client
// encoder of ADR-0129 public ids is checked against.
func TestCanonicalID_ServicekitVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/pubid_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Kind, UUID, Public string }
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors {
		if got := canonicalID(v.Kind, v.Public); got != v.UUID {
			t.Errorf("canonicalID(%q) = %q, want %q", v.Public, got, v.UUID)
		}
	}
}

func TestCanonicalID_LeavesNonIDsAlone(t *testing.T) {
	for _, c := range [][2]string{
		{"ten", "*"},
		{"ten", "my-bucket"},
		{"ten", "ten_"},
		{"ten", "ten_111"},
		{"ten", "ten_" + strings.Repeat("1", 23)}, // wrong width
		{"ten", "ten_0OIl111111111111111111"},     // 0, O, I, l are outside the alphabet
		{"ten", "TEN_1111111111111111111111"},     // the prefix is case-sensitive
		{"ten", "ins_1111111111111111111111"},     // another kind
		{"ins", "zzz_1111111111111111111111"},     // unregistered prefix
		{"ten", "ten_zzzzzzzzzzzzzzzzzzzzzz"},     // above 2^128
	} {
		if got := canonicalID(c[0], c[1]); got != c[1] {
			t.Errorf("canonicalID(%q, %q) = %q, want it unchanged", c[0], c[1], got)
		}
	}
	if got := canonicalID("ten", "A0EEBC999C0B4EF8BB6D6BB9BD380A11"); got != "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" {
		t.Errorf("lax UUID spelling not folded: %q", got)
	}
}

// iam_nouns.json must cover the nouns the docs and tests rely on, and only
// registered-looking prefixes.
func TestIAMNouns_Table(t *testing.T) {
	if len(iamNouns) == 0 {
		t.Fatal("empty noun table")
	}
	for noun, want := range map[string]string{"compute:instances": "ins", "storage:volumes": "vol", "network:dns-zones": "dnz"} {
		if iamNouns[noun] != want {
			t.Errorf("iamNouns[%s] = %q, want %q", noun, iamNouns[noun], want)
		}
	}
	for noun, prefix := range iamNouns {
		if prefix == "" {
			t.Errorf("noun %s has an empty prefix", noun)
		}
	}
	// servicekit authz nameKeyedTypes: the evaluator never folds these ids.
	for _, nameKeyed := range []string{
		"storage:buckets", "storage:objects", "storage:credentials", "compute:ssh-keys",
		"registry:repositories", "registry:artifacts", "registry:caches", "registry:credentials",
		"messaging:vhosts", "messaging:users", "messaging:permissions",
	} {
		if _, ok := iamNouns[nameKeyed]; ok {
			t.Errorf("name-keyed noun %s must not be folded", nameKeyed)
		}
	}
}

const (
	cfgTenant = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	cfgID     = "00000000-0000-0000-0000-000000000001"
)

func policyDoc(target, tenantConstraint string) string {
	c := ""
	if tenantConstraint != "" {
		c = `,"constraints":{"equals":{"frn:tenant":"` + tenantConstraint + `"}}`
	}
	return `{"schemaVersion":"1","rules":[{"access":"deny","operations":["compute:instances:read"],"targets":["` + target + `"]` + c + `}]}`
}

func TestSameJSONDoc_PublicAndUUIDFormsAreTheSamePolicy(t *testing.T) {
	ten := encodeForTest(t, "ten", cfgTenant)
	vol := encodeForTest(t, "vol", cfgID)
	cfg := policyDoc("frn:storage:*:"+cfgTenant+":volumes/"+cfgID, cfgTenant)
	server := policyDoc("frn:storage:*:"+ten+":volumes/"+vol, ten)
	if !sameServerDoc(cfg, server) {
		t.Error("the normalised server document does not match its UUID-form config")
	}
	if !sameServerDoc("["+cfg+"]", "["+server+"]") {
		t.Error("policy-set form not folded")
	}
	other := encodeForTest(t, "ten", cfgID)
	for name, diff := range map[string]string{
		"another tenant":            policyDoc("frn:storage:*:"+other+":volumes/"+vol, ten),
		"another id":                policyDoc("frn:storage:*:"+ten+":volumes/"+encodeForTest(t, "vol", cfgTenant), ten),
		"another constraint tenant": policyDoc("frn:storage:*:"+ten+":volumes/"+vol, other),
		"a wildcard id":             policyDoc("frn:storage:*:"+ten+":volumes/*", ten),
		"another kind's prefix":     policyDoc("frn:storage:*:"+ten+":volumes/"+encodeForTest(t, "ins", cfgID), ten),
		"an unregistered prefix":    policyDoc("frn:storage:*:"+ten+":volumes/"+encodeForTest(t, "zzz", cfgID), ten),
		"a non-tenant constraint":   policyDoc("frn:storage:*:"+ten+":volumes/"+vol, encodeForTest(t, "zzz", cfgTenant)),
	} {
		if sameServerDoc(cfg, diff) {
			t.Errorf("%s compared equal", name)
		}
	}
	// A config-to-config comparison never folds: a respelt id is a change to send.
	if sameJSONDoc(cfg, server) {
		t.Error("sameJSONDoc folded")
	}
}

// Name-keyed nouns keep their ids exact: these are different resources the
// evaluator never folds, so the read-back must not call them equal.
func TestSameServerDoc_NameKeyedIDsStayExact(t *testing.T) {
	body := encodeForTest(t, "x", cfgID)[2:]
	for name, pair := range map[string][2]string{
		"bucket hyphens": {"frn:storage:*:*:buckets/" + cfgTenant, "frn:storage:*:*:buckets/" + strings.ReplaceAll(cfgTenant, "-", "")},
		"ssh key prefix": {"frn:compute:*:*:ssh-keys/deploy_" + body, "frn:compute:*:*:ssh-keys/build_" + body},
	} {
		if sameServerDoc(policyDoc(pair[0], ""), policyDoc(pair[1], "")) {
			t.Errorf("%s: two different name-keyed targets compared equal", name)
		}
	}
}

// Refresh: a UUID-form state survives the server's public form.
func TestFromAPI_KeepsUUIDFormStateAgainstNormalisedServer(t *testing.T) {
	cfg := policyDoc("frn:compute:*:"+cfgTenant+":instances/"+cfgID, "")
	server := policyDoc("frn:compute:*:"+encodeForTest(t, "ten", cfgTenant)+":instances/"+encodeForTest(t, "ins", cfgID), "")
	m := IAMPolicyModel{Description: types.StringNull(), Document: types.StringValue(cfg)}
	m.fromAPI(&apiPolicy{ID: "pol-1", Name: "x", Document: json.RawMessage(server)})
	if m.Document.ValueString() != cfg {
		t.Errorf("state document %s, want %s", m.Document.ValueString(), cfg)
	}
}

// A config that only respells an id is still sent: the update must not
// collapse to a tags-only PATCH.
func TestOnlyTagsDiffer_RespeltIDIsADocumentChange(t *testing.T) {
	a := policyDoc("frn:compute:*:"+cfgTenant+":instances/"+cfgID, "")
	b := policyDoc("frn:compute:*:"+encodeForTest(t, "ten", cfgTenant)+":instances/"+encodeForTest(t, "ins", cfgID), "")
	state := &IAMPolicyModel{Name: types.StringValue("x"), Description: types.StringNull(), Document: types.StringValue(a)}
	plan := &IAMPolicyModel{Name: types.StringValue("x"), Description: types.StringNull(), Document: types.StringValue(b)}
	if onlyTagsDiffer(plan, state) {
		t.Error("a respelt document was treated as a tags-only change")
	}
}

// encodeForTest renders a public id from the vectors' rule (base58, fixed width
// 22), independent of the decoder under test.
func encodeForTest(t *testing.T, prefix, id string) string {
	t.Helper()
	hex := strings.ReplaceAll(id, "-", "")
	var digits []int
	n := make([]int, 0, 32)
	for _, c := range hex {
		n = append(n, strings.IndexRune("0123456789abcdef", c))
	}
	// repeated division of the base-16 digit string by 58
	for len(n) > 0 {
		rem, q := 0, []int{}
		for _, d := range n {
			acc := rem*16 + d
			if len(q) > 0 || acc/58 > 0 {
				q = append(q, acc/58)
			}
			rem = acc % 58
		}
		digits = append(digits, rem)
		n = q
	}
	for len(digits) < publicIDBodyLen {
		digits = append(digits, 0)
	}
	var b strings.Builder
	for i := len(digits) - 1; i >= 0; i-- {
		b.WriteByte(base58Alphabet[digits[i]])
	}
	return prefix + "_" + b.String()
}

func TestEncodeForTest_MatchesVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/pubid_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Kind, UUID, Public string }
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		if got := encodeForTest(t, v.Kind, v.UUID); got != v.Public {
			t.Errorf("encodeForTest(%s, %s) = %q, want %q", v.Kind, v.UUID, got, v.Public)
		}
	}
}

// identity 3b stores a UUID-form target as its public form and returns it so.
// The state must keep the planned document, or Terraform fails the apply with
// "Provider produced inconsistent result after apply".
func TestResource_Create_ServerNormalisedDocumentKeepsThePlannedOne(t *testing.T) {
	cfg := policyDoc("frn:storage:*:"+cfgTenant+":volumes/"+cfgID, "")
	normalised := policyDoc("frn:storage:*:"+encodeForTest(t, "ten", cfgTenant)+":volumes/"+encodeForTest(t, "vol", cfgID), "")
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/iam/policies" {
			var req apiCreatePolicyRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPolicy{
				ID: "pol-new", Name: req.Name, Document: json.RawMessage(normalised), TenantID: "tenant-1",
				AuthoredBy: "user-1", Version: 1, CreatedAt: "2026-10-19T00:00:00Z", UpdatedAt: "2026-10-19T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := policySchema(t)
	planVal := tftypes.NewValue(s.Type().TerraformType(ctx), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "deny-volume"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"document":    tftypes.NewValue(tftypes.String, cfg),
		"tenant_id":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"authored_by": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"version":     tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"tags_all":    tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, tftypes.UnknownValue),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	res.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var m IAMPolicyModel
	resp.State.Get(ctx, &m)
	if m.Document.ValueString() != cfg {
		t.Errorf("state document %s, want the planned %s", m.Document.ValueString(), cfg)
	}
}
