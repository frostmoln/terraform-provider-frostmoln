package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var defaultTagsAttrTypes = map[string]attr.Type{"tags": types.MapType{ElemType: types.StringType}}

func defaultTagsBlock(tags types.Map) types.Object {
	return types.ObjectValueMust(defaultTagsAttrTypes, map[string]attr.Value{"tags": tags})
}

func TestResolveDefaultTags(t *testing.T) {
	known := types.MapValueMust(types.StringType, map[string]attr.Value{
		"env": types.StringValue("prod"), "unset": types.StringNull(),
	})
	partial := types.MapValueMust(types.StringType, map[string]attr.Value{
		"env": types.StringValue("prod"), "later": types.StringUnknown(),
	})

	tests := []struct {
		name        string
		block       types.Object
		wantTags    map[string]string
		wantUnknown bool
	}{
		{name: "absent block", block: types.ObjectNull(defaultTagsAttrTypes), wantTags: nil},
		{name: "null tags", block: defaultTagsBlock(types.MapNull(types.StringType)), wantTags: nil},
		{name: "known tags; a null value is no default", block: defaultTagsBlock(known), wantTags: map[string]string{"env": "prod"}},
		// Unknown is never an error: the plan predicts tags_all as unknown and
		// the apply-time configure sees the value (the AWS provider's behaviour
		// for a computed default tag).
		{name: "unknown block (a dynamic block over an unknown for_each)", block: types.ObjectUnknown(defaultTagsAttrTypes), wantUnknown: true},
		{name: "unknown tags map", block: defaultTagsBlock(types.MapUnknown(types.StringType)), wantUnknown: true},
		{name: "one value unknown", block: defaultTagsBlock(partial), wantTags: map[string]string{"env": "prod"}, wantUnknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var diags diag.Diagnostics
			got := resolveDefaultTags(tt.block, &diags)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if got.Unknown != tt.wantUnknown {
				t.Errorf("Unknown = %v, want %v", got.Unknown, tt.wantUnknown)
			}
			if len(got.Tags) != len(tt.wantTags) {
				t.Fatalf("Tags = %v, want %v", got.Tags, tt.wantTags)
			}
			for k, v := range tt.wantTags {
				if got.Tags[k] != v {
					t.Errorf("Tags[%q] = %q, want %q", k, got.Tags[k], v)
				}
			}
		})
	}
}

// configureWithDefaults runs the ConfigureProvider RPC with the given
// default_tags against a /v1/me fake and returns its error diagnostics.
func configureWithDefaults(t *testing.T, tags map[string]string) []*tfprotov6.Diagnostic {
	t.Helper()
	server := meServer(t, "t-1")
	t.Cleanup(server.Close)
	dt := tftypes.NewValue(defaultTagsType, map[string]tftypes.Value{"tags": stringMap(tags)})
	endpoint, apiKey, noCLI := server.URL, "k", false // pragma: allowlist secret
	resp, err := providerserver.NewProtocol6(New("test")())().ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey, useCLIConfig: &noCLI, defaultTags: &dt}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	var errs []*tfprotov6.Diagnostic
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			errs = append(errs, d)
		}
	}
	return errs
}

// TestDefaultTagsValidationParity: the provider refuses exactly what identity
// refuses for a tenant's default tags (identity internal/domain/tag_settings.go
// ValidateTenantDefaultTags, since v5.8.0) — identity's own test vectors, one
// per rule class, through the real configure. Each refusal names the key and
// sits on that key's attribute path; a legal key alongside is not reported.
func TestDefaultTagsValidationParity(t *testing.T) {
	cases := []struct {
		name, key, value, summary string
	}{
		{"platform prefix", "frostmoln_type", "v", "Reserved Default Tag Key"},
		{"platform prefix, other separator and case", "FROSTMOLN-X", "v", "Reserved Default Tag Key"},
		{"instance-reserved prefix", "os_type", "v", "Reserved Default Tag Key"},
		{"instance-reserved prefix, folded", "Nova_x", "v", "Reserved Default Tag Key"},
		{"instance_ prefix", "instance_name", "v", "Reserved Default Tag Key"},
		{"storage control key, folded", "Customer-Id", "v", "Reserved Default Tag Key"},
		{"bucket control key", "storage-class", "v", "Reserved Default Tag Key"},
		{"bucket control key, folded", "ACL", "v", "Reserved Default Tag Key"},
		{"key too long", strings.Repeat("k", 65), "v", "Invalid Default Tag Key"},
		{"slash in key", "team/owner", "v", "Invalid Default Tag Key"},
		{"space in key", "my key", "v", "Invalid Default Tag Key"},
		{"key must start alphanumeric", "-lead", "v", "Invalid Default Tag Key"},
		{"key must end alphanumeric", "trail.", "v", "Invalid Default Tag Key"},
		{"non-ASCII key", "miljö", "v", "Invalid Default Tag Key"},
		{"value too long", "env", strings.Repeat("v", 256), "Invalid Default Tag Value"},
		{"quote in value", "env", `say "hi"`, "Invalid Default Tag Value"},
		{"backslash in value", "env", `a\b`, "Invalid Default Tag Value"},
		{"comma in value", "env", "a,b", "Invalid Default Tag Value"},
		{"non-ASCII value", "env", "ünï", "Invalid Default Tag Value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearCredentialEnv(t)
			errs := configureWithDefaults(t, map[string]string{tc.key: tc.value, "ok": "fine"})
			if len(errs) != 1 {
				t.Fatalf("got %d error diagnostics, want exactly the one for %q: %v", len(errs), tc.key, errs)
			}
			d := errs[0]
			if d.Summary != tc.summary || !strings.Contains(d.Detail, tc.key) {
				t.Errorf("got %s: %s — want %s naming %q", d.Summary, d.Detail, tc.summary, tc.key)
			}
			want := tftypes.NewAttributePath().WithAttributeName("default_tags").WithAttributeName("tags").WithElementKeyString(tc.key)
			if d.Attribute == nil || !d.Attribute.Equal(want) {
				t.Errorf("diagnostic path = %v, want %v", d.Attribute, want)
			}
		})
	}

	t.Run("the intersection is accepted", func(t *testing.T) {
		clearCredentialEnv(t)
		ok := map[string]string{
			"env": "prod", "cost-center": "eu.team:42", "a": "", "Team_Name.v2": "Platform Ops",
			"k1": "user@example.com", "path": "a/b/c", "eq": "x=y+z", strings.Repeat("k", 64): strings.Repeat("v", 255),
			"frostmoln": "v", "acls": "v",
		}
		if errs := configureWithDefaults(t, ok); len(errs) != 0 {
			t.Errorf("legal defaults refused: %v", errs)
		}
	})

	t.Run("at most ten", func(t *testing.T) {
		clearCredentialEnv(t)
		tags := map[string]string{}
		for i := 0; i < 10; i++ {
			tags[fmt.Sprintf("k%d", i)] = "v"
		}
		if errs := configureWithDefaults(t, tags); len(errs) != 0 {
			t.Fatalf("exactly ten refused: %v", errs)
		}
		tags["one-more"] = "v"
		errs := configureWithDefaults(t, tags)
		if len(errs) != 1 || errs[0].Summary != "Too Many Default Tags" {
			t.Errorf("eleven: got %v, want one Too Many Default Tags", errs)
		}
	})
}

// configureDirect runs Configure against a /v1/me fake and returns the client
// every resource receives.
func configureDirect(t *testing.T, defaultTags tftypes.Value) (*client.Client, diag.Diagnostics) {
	t.Helper()
	clearCredentialEnv(t)
	server := meServer(t, "t-1")
	t.Cleanup(server.Close)

	p := &FrostmolnProvider{version: "test"}
	var sr provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &sr)

	endpoint, apiKey := server.URL, "k" // pragma: allowlist secret
	dv := newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey, defaultTags: &defaultTags})
	raw, err := dv.Unmarshal(providerConfigType())
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	var resp provider.ConfigureResponse
	p.Configure(context.Background(), provider.ConfigureRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: raw}}, &resp)
	c, _ := resp.ResourceData.(*client.Client)
	return c, resp.Diagnostics
}

func TestConfigureHandsDefaultTagsToResources(t *testing.T) {
	dt := tftypes.NewValue(defaultTagsType, map[string]tftypes.Value{"tags": stringMap(map[string]string{"env": "prod"})})
	c, diags := configureDirect(t, dt)
	if diags.HasError() {
		t.Fatalf("configure: %v", diags)
	}
	if c == nil {
		t.Fatal("no client handed to resources")
	}
	got := c.DefaultTags()
	if got.Unknown || len(got.Tags) != 1 || got.Tags["env"] != "prod" {
		t.Errorf("resources see default_tags %+v, want {env=prod}", got)
	}
}

// TestConfigureAcceptsUnknownDefaultTags: never an error and never a panic —
// the plan marks tags_all unknown instead, and the apply configures again with
// the known value.
func TestConfigureAcceptsUnknownDefaultTags(t *testing.T) {
	mapType := tftypes.Map{ElementType: tftypes.String}
	for name, dt := range map[string]tftypes.Value{
		"unknown block": tftypes.NewValue(defaultTagsType, tftypes.UnknownValue),
		"unknown map":   tftypes.NewValue(defaultTagsType, map[string]tftypes.Value{"tags": tftypes.NewValue(mapType, tftypes.UnknownValue)}),
		"unknown value": tftypes.NewValue(defaultTagsType, map[string]tftypes.Value{"tags": tftypes.NewValue(mapType, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		})}),
	} {
		t.Run(name, func(t *testing.T) {
			c, diags := configureDirect(t, dt)
			if diags.HasError() {
				t.Fatalf("configure failed on an unknown default_tags: %v", diags)
			}
			if c == nil || !c.DefaultTags().Unknown {
				t.Error("resources must see default_tags as unknown so the plan predicts tags_all as unknown")
			}
		})
	}
}
