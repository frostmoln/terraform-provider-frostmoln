package provider

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// The description-behavior contract (Mechanic 4 of the surface contract
// program). A schema Description is CUSTOMER DOCUMENTATION: it renders verbatim
// to registry.terraform.io and is imported into docs.frostmoln.se, so a wrong
// claim in one is a wrong claim everywhere. Three fail-closed rules:
//
//  1. WRITE-ONLY DISCIPLINE. "write-only" is a formal Terraform 1.11 concept —
//     a framework WriteOnly attribute whose value reaches the provider on apply
//     and is never written to plan or state. A description that uses the phrase
//     must sit on an attribute that IS WriteOnly; a colloquial "the API never
//     returns it, therefore write-only" (the phrasing PR #429 had to remove
//     from three attributes, and the exact misconception docs.frostmoln.se's
//     stale mirror still ships) must not come back.
//
//  2. ONE PARAGRAPH PER ATTRIBUTE. tfplugindocs renders an attribute
//     description as a bullet in the resource page's `### Optional` list; a
//     blank line terminates that list, and the continuation paragraphs float
//     free at column zero — on registry and docs.frostmoln.se alike they read
//     as if they belonged to the whole section (the degraded
//     instance.user_data pages, work item 01a06218-7132). Long-form prose
//     belongs in the resource-level Description, which renders as its own
//     section. Blank lines inside code fences are excluded: fenced examples
//     cannot terminate the bullet list.
//
//  3. PINNED BEHAVIOR SENTENCES. The drift-prone attribute audit (this leg's
//     scope, gate e) corrected claims that were WRONG — descriptions promising
//     behavior the backend does not have. Each corrected claim is pinned here
//     verbatim (substring), so a description "simplification" cannot silently
//     restore fiction that customers rely on. Compare
//     scope_declarations_test.go: that walker enforces create-immutability
//     prose at RESOURCE level (scopedecl); this one enforces the
//     attribute-level claims the scopedecl summary does not carry.

var descriptionWriteOnlyPhrase = regexp.MustCompile(`(?i)write[- ]only`)

// writeOnlyPhraseAllowlist carries non-WriteOnly attributes whose description
// uses the phrase to talk about a DIFFERENT, genuinely write-only attribute —
// the *_wo_version companions, whose whole job is to describe how their
// write-only sibling is (not) observed. One entry per attribute, with the
// reason; anything else using the phrase on a non-WriteOnly attribute fails,
// which is what killed the "the API does not return it, therefore write-only"
// sense PR #429 removed from three attributes and docs.frostmoln.se's stale
// mirror still ships.
var writeOnlyPhraseAllowlist = map[string]string{
	"frostmoln_instance:console_password_wo_version":         "companion: 'the write-only password' refers to its console_password_wo sibling",
	"frostmoln_instance:user_data_wo_version":                "companion: 'the write-only document' refers to its user_data_wo sibling",
	"frostmoln_launch_template:user_data_wo_version":         "companion: refers to the user_data_wo sibling",
	"frostmoln_secret:secret_value_wo_version":               "companion: refers to the secret_value_wo sibling",
	"frostmoln_appgw_certificate:private_key_pem_wo_version": "companion: refers to the private_key_pem_wo sibling",
	"frostmoln_container_registry_cache:password_wo_version": "companion: refers to the password_wo sibling",
}

// descriptionDebtTable lists the attribute descriptions that still carry a
// blank-line paragraph break UNINDENTED outside code fences — the 01a06218-7132
// rendering class at scale (tfplugindocs renders continuation paragraphs at
// column zero, which terminates the attribute's bullet and lets the prose float
// free on registry and docs.frostmoln.se). The fix, proven on
// instance.user_data in this PR: indent each continuation paragraph four
// spaces; the rendered page keeps it inside the bullet.
//
// The table is checked in both directions: an entry that no longer violates
// fails (delete it), and a violation that is not in the table fails (new prose
// must land indented — the class cannot grow). Mass reindent is its own
// follow-up under program 01a08019 (2026-09-11 parity-CI session); this leg
// fixes the two attributes the work item named and stops the class here.
var descriptionDebtTable = []string{
	"frostmoln_apache_instance:public",
	"frostmoln_appgw_backend:source_kind",
	"frostmoln_appgw_backend:status",
	"frostmoln_appgw_backend_authorization:adopt_existing",
	"frostmoln_appgw_backend_pool:proxy_protocol",
	"frostmoln_appgw_backend_pool:tls_verify_backend",
	"frostmoln_appgw_config_apply:triggers",
	"frostmoln_appgw_flavors:flavors.max_waf_exclusions",
	"frostmoln_appgw_health_check:port",
	"frostmoln_appgw_health_check:protocol",
	"frostmoln_appgw_health_check:proxy_protocol",
	"frostmoln_appgw_listener:backend_pool_id",
	"frostmoln_appgw_listener:port_range_end",
	"frostmoln_appgw_listener:protocol",
	"frostmoln_appgw_listener:rate_limit_rps",
	"frostmoln_appgw_waf_exclusion:description",
	"frostmoln_appgw_waf_policy:allowed_methods",
	"frostmoln_appgw_waf_policy:allowed_request_content_types",
	"frostmoln_appgw_waf_policy:anomaly_score_threshold",
	"frostmoln_appgw_waf_policy:effective_allowed_methods",
	"frostmoln_appgw_waf_policy:effective_allowed_request_content_types",
	"frostmoln_appgw_waf_policy:effective_mode",
	"frostmoln_appgw_waf_policy:managed_ruleset_version",
	"frostmoln_appgw_waf_policy:mode",
	"frostmoln_appgw_waf_policy:paranoia_level",
	"frostmoln_appgw_waf_policy:request_body_limit_bytes",
	"frostmoln_appgw_waf_policy:scope",
	"frostmoln_appgw_waf_policy_attachment:effective_mode",
	"frostmoln_appgw_waf_policy_publication:effective_mode",
	"frostmoln_appgw_waf_policy_publication:max_newly_blocked",
	"frostmoln_appgw_waf_policy_publication:platform_opt_outs",
	"frostmoln_appgw_waf_rule:builder_json",
	"frostmoln_appgw_waf_rule:kind",
	"frostmoln_appgw_waf_rule:raw",
	"frostmoln_appgw_waf_rule:revision",
	"frostmoln_application_gateway:public_ip_id",
	"frostmoln_application_gateway:public_ip_mode",
	"frostmoln_container_registry_artifacts:artifacts.pulled_at",
	"frostmoln_container_registry_artifacts:artifacts.size_bytes",
	"frostmoln_container_registry_artifacts:artifacts.tags",
	"frostmoln_gateway:acknowledge_connectivity_loss",
	"frostmoln_gateway:mode",
	"frostmoln_gateway:public_ip_id",
	"frostmoln_kubernetes_cluster:public_ip_id",
	"frostmoln_load_balancer:public_ip_id",
	"frostmoln_nginx_instance:public",
	"frostmoln_public_ip:acknowledge_address_loss",
	"frostmoln_public_ip:attachment",
	"frostmoln_public_ip:attachment.kind",
	"frostmoln_public_ip:instance_id",
	"frostmoln_public_ip_association:port_id",
	"frostmoln_security_group:delete_default_egress",
	"frostmoln_vpc_route:destination",
	"frostmoln_vpc_route:next_hop",
	"frostmoln_workload_identity_binding:scopes",
}

// pinnedBehaviorSentences lists, per resource type, per attribute path, the
// substrings a description MUST carry (the corrected contract in the few words
// a reader needs). Checked in both directions: a listed path that no longer
// exists fails, and an attribute is only pinned by explicit entry — nothing
// structural is inferred, so the pin table cannot rot into noise.
var pinnedBehaviorSentences = map[string]map[string][]string{
	"frostmoln_instance": {
		"security_groups": {
			// 01a041f8-98ef: the schema said Optional and the old copy promised
			// a create-time clear-fallback the backend refuses inside the saga
			// when the port is pinned. Both the create constraint (WITH the
			// subnet_id conditional — without it the omission is legal) and the
			// update-only clear path are load-bearing.
			"whenever `subnet_id` is set",
			"requires at least one security group",
			"security_group_ids is required",
			"clears ALL security groups",
			"when you set `security_groups` in your configuration",
		},
	},
	"frostmoln_security_group": {
		"description": {
			// 01a041f8-4738: network's neutron layer reserves the description
			// field for its internal metadata blob and drops the customer
			// value — the old copy ("A description of the security group.")
			// implied persistence the platform does not have.
			"does not persist it yet",
			"reappears as a pending change",
		},
	},
}

// attrDescription is the package-neutral projection the shared rules check:
// the customer-visible text plus whether the attribute carries the formal
// WriteOnly flag.
type attrDescription struct {
	path      string
	text      string
	writeOnly bool
}

// checkAttributeDescription applies rules 1-3 to one projected attribute.
func checkAttributeDescription(t *testing.T, typeName string, attr attrDescription, pinnedSeen, multiParagraphSeen map[string]bool) {
	t.Helper()

	if substrings, pinned := pinnedBehaviorSentences[typeName][attr.path]; pinned {
		pinnedSeen[typeName+":"+attr.path] = true
		for _, want := range substrings {
			if !strings.Contains(attr.text, want) {
				t.Errorf("%s.%s description lost its pinned behavior claim; must contain %q\n--- current:\n%s", typeName, attr.path, want, attr.text)
			}
		}
	}

	if loc := descriptionWriteOnlyPhrase.FindStringIndex(attr.text); loc != nil && !attr.writeOnly {
		key := typeName + ":" + attr.path
		if _, allowed := writeOnlyPhraseAllowlist[key]; !allowed {
			t.Errorf("%s.%s description uses the phrase %q but the attribute is not WriteOnly — "+
				"\"write-only\" is the formal Terraform 1.11 concept (never written to plan or state); "+
				"\"the API does not return it\" must be stated in those words instead. Speak of a _wo SIBLING "+
				"only via writeOnlyPhraseAllowlist (state the reason)",
				typeName, attr.path, attr.text[loc[0]:loc[1]])
		}
	}

	// One-paragraph-per-bullet rule: outside code fences, a blank line followed
	// by an UNINDENTED line terminates the attribute's rendered list item; the
	// continuation must be indented (four spaces — proven to survive
	// tfplugindocs and prettier) so it renders inside the bullet. Current
	// violations are enumerable debt (descriptionDebtTable), not a carve-out:
	// both directions are checked.
	if line := unindentedContinuation(attr.text); line != "" {
		key := typeName + ":" + attr.path
		if !containsString(descriptionDebtTable, key) {
			t.Errorf("%s.%s description continues after a blank line at column zero (%q) — "+
				"indent the continuation paragraph four spaces so it renders inside the attribute's bullet",
				typeName, attr.path, clipLine(line))
		}
		multiParagraphSeen[key] = true
	}
}

// TestAttributeDescriptionContract enforces the three rules against every
// resource AND data source — both render to customer surfaces. The two
// framework schema packages do not share an interface, so each flavor walks
// its own types and feeds the shared projection; the rules live in
// checkAttributeDescription, once.
func TestAttributeDescriptionContract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	pinnedSeen := map[string]bool{}
	multiParagraphSeen := map[string]bool{}

	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var mdResp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp)
		var schemaResp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
		t.Run(mdResp.TypeName, func(t *testing.T) {
			for _, attr := range walkResourceDescriptions(t, "", schemaResp.Schema.Attributes, schemaResp.Schema.Blocks) {
				checkAttributeDescription(t, mdResp.TypeName, attr, pinnedSeen, multiParagraphSeen)
			}
		})
	}
	for _, newDS := range p.DataSources(ctx) {
		d := newDS()
		var mdResp datasourceMD
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp.resp)
		var schemaResp datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
		t.Run(mdResp.name(), func(t *testing.T) {
			for _, attr := range walkDataSourceDescriptions(t, "", schemaResp.Schema.Attributes, schemaResp.Schema.Blocks) {
				checkAttributeDescription(t, mdResp.name(), attr, pinnedSeen, multiParagraphSeen)
			}
		})
	}

	// Both directions on the pin table: an entry that names a dead attribute
	// or resource must not survive silently.
	for resourceType, paths := range pinnedBehaviorSentences {
		for attrPath := range paths {
			if !pinnedSeen[resourceType+":"+attrPath] {
				t.Errorf("pinnedBehaviorSentences pins %q, which is not an attribute of that resource — update the pin table", resourceType+":"+attrPath)
			}
		}
	}
	// And on the debt table: an entry whose attribute no longer violates is a
	// lie in the other direction — delete it when you fix it.
	for _, key := range descriptionDebtTable {
		if !multiParagraphSeen[key] {
			t.Errorf("descriptionDebtTable lists %q, which no longer carries an unindented continuation — delete the entry", key)
		}
	}
}

// datasourceMD carries the data-source type name out of the Metadata response
// without re-requesting it.
type datasourceMD struct {
	resp datasource.MetadataResponse
}

func (m datasourceMD) name() string { return m.resp.TypeName }

// walkResourceDescriptions flattens every attribute reachable from a resource
// schema — nested attributes and blocks descended, dotted paths, fail-closed
// on unknown block shapes, like every walker in this package.
func walkResourceDescriptions(t *testing.T, prefix string, attrs map[string]rschema.Attribute, blocks map[string]rschema.Block) []attrDescription {
	t.Helper()
	out := make([]attrDescription, 0, len(attrs))

	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a := attrs[name]
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}
		switch nested := a.(type) {
		case rschema.SingleNestedAttribute:
			// Fail-closed: tfplugindocs prefers MarkdownDescription when set, and
			// the rules below read only GetDescription — a container populating it
			// would silently escape. No schema attribute sets it today.
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkResourceDescriptions(t, attrPath, nested.Attributes, nil)...)
		case rschema.ListNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkResourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nil)...)
		case rschema.SetNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkResourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nil)...)
		case rschema.MapNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkResourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nil)...)
		default:
			// Fail-closed on a novel shape: a new nested-attribute kind from a
			// framework bump must not be silently walked as if it had no
			// children. Leaf attribute types (String/Bool/Number/Int64/Float64/
			// primitive Set/List/Map/Dynamic) carry no nested surface and no
			// MarkdownDescription field, so they are the only expected shapes here.
			if hasUntypedNestedObject(a) {
				t.Fatalf("%s: unsupported nested attribute type %T — extend walkResourceDescriptions", attrPath, a)
			}
		}
		out = append(out, attrDescription{path: attrPath, text: a.GetDescription(), writeOnly: resourceWriteOnly(a)})
	}

	blockNames := make([]string, 0, len(blocks))
	for name := range blocks {
		blockNames = append(blockNames, name)
	}
	sort.Strings(blockNames)
	for _, name := range blockNames {
		b := blocks[name]
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}
		switch nested := b.(type) {
		case rschema.SingleNestedBlock:
			out = append(out, walkResourceDescriptions(t, attrPath, nested.Attributes, nested.Blocks)...)
		case rschema.ListNestedBlock:
			out = append(out, walkResourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nested.NestedObject.Blocks)...)
		default:
			t.Fatalf("%s: unsupported block type %T — extend walkResourceDescriptions", attrPath, b)
		}
	}
	return out
}

// walkDataSourceDescriptions is the data-source twin of
// walkResourceDescriptions (the framework's two schema packages share no
// interface; the shapes below are otherwise identical).
func walkDataSourceDescriptions(t *testing.T, prefix string, attrs map[string]dschema.Attribute, blocks map[string]dschema.Block) []attrDescription {
	t.Helper()
	out := make([]attrDescription, 0, len(attrs))

	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a := attrs[name]
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}
		switch nested := a.(type) {
		case dschema.SingleNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkDataSourceDescriptions(t, attrPath, nested.Attributes, nil)...)
		case dschema.ListNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkDataSourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nil)...)
		case dschema.SetNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkDataSourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nil)...)
		case dschema.MapNestedAttribute:
			if nested.MarkdownDescription != "" {
				t.Fatalf("%s: nested attribute carries MarkdownDescription, which the description rules do not read — extend them", attrPath)
			}
			out = append(out, walkDataSourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nil)...)
		default:
			if hasUntypedNestedObjectDS(a) {
				t.Fatalf("%s: unsupported nested attribute type %T — extend walkDataSourceDescriptions", attrPath, a)
			}
		}
		out = append(out, attrDescription{path: attrPath, text: a.GetDescription(), writeOnly: dataSourceWriteOnly(a)})
	}

	blockNames := make([]string, 0, len(blocks))
	for name := range blocks {
		blockNames = append(blockNames, name)
	}
	sort.Strings(blockNames)
	for _, name := range blockNames {
		b := blocks[name]
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}
		switch nested := b.(type) {
		case dschema.SingleNestedBlock:
			out = append(out, walkDataSourceDescriptions(t, attrPath, nested.Attributes, nested.Blocks)...)
		case dschema.ListNestedBlock:
			out = append(out, walkDataSourceDescriptions(t, attrPath, nested.NestedObject.Attributes, nested.NestedObject.Blocks)...)
		default:
			t.Fatalf("%s: unsupported block type %T — extend walkDataSourceDescriptions", attrPath, b)
		}
	}
	return out
}

// hasUntypedNestedObject reports whether the attribute shape carries a nested
// object surface the switch above did not descend into. Nested-attribute and
// collection-of-object types share this interface; plain primitive leaves do
// not implement it, which is what keeps leaf attrs out of the fatal path.
func hasUntypedNestedObject(a rschema.Attribute) bool {
	_, nested := a.(interface {
		GetNestedObject() rschema.NestedAttributeObject
	})
	return nested
}

func hasUntypedNestedObjectDS(a dschema.Attribute) bool {
	_, nested := a.(interface {
		GetNestedObject() dschema.NestedAttributeObject
	})
	return nested
}

// resourceWriteOnly reports the formal WriteOnly flag. Only the framework
// types that support write-only can carry it; everything else is false, which
// is exactly what rule 1 wants a phrase-hunter to see.
func resourceWriteOnly(a rschema.Attribute) bool {
	switch a := a.(type) {
	case rschema.StringAttribute:
		return a.WriteOnly
	case rschema.BoolAttribute:
		return a.WriteOnly
	case rschema.NumberAttribute:
		return a.WriteOnly
	default:
		return false
	}
}

// dataSourceWriteOnly: write-only is a write-side concept — the data source
// schema package carries no WriteOnly flag at all, so no data-source attribute
// can lawfully use the phrase (which is exactly the check rule 1 wants).
func dataSourceWriteOnly(a dschema.Attribute) bool {
	_ = a
	return false
}

// nonFencedBlankParagraph → renamed: unindentedContinuation returns the first
// line that follows a blank line without indentation outside code fences, or
// "" — indented or fenced continuations render inside the attribute's bullet.
func unindentedContinuation(desc string) string {
	inFence := false
	blank := false
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			blank = false
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			blank = true
			continue
		}
		if blank && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			return line
		}
		blank = false
	}
	return ""
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
