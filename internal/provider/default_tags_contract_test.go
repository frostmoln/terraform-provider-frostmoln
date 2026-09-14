package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// defaultTagsExclusions lists taggable resources that do NOT apply the
// provider's default_tags, each with the reason its documentation gives.
// Checked in both directions like allowedInlineCollections: an entry for a
// resource that is not taggable fails, and a taggable resource missing from
// tagProfiles fails unless it is listed here.
//
// Empty: frostmoln_snapshot, the one candidate, updates its tags in place
// (storage since v1.23.0), so it takes the defaults like everything else.
var defaultTagsExclusions = map[string]string{}

// tagsIsASetting lists resources whose `tags` attribute is NOT tags on the
// resource itself but a tag SETTING it manages, each with the reason. They are
// not taggable: they take no tags_all, run no default_tags matrix, and the
// provider's default_tags never merge into them. Checked in both directions:
// an entry must be a registered resource with `tags`, and must NOT carry
// tags_all (which would mean it had become taggable after all).
var tagsIsASetting = map[string]string{
	"frostmoln_tenant_default_tags": "`tags` is the tenant's default-tag set — the tags the platform copies " +
		"onto resources created in the tenant — not tags on a resource; merging the provider's default_tags " +
		"into it would turn every provider default into a tenant-wide one",
}

// tagProfiles is every taggable resource's wire profile for the fake backend
// (internal/tftags/tftagstest). A resource with `tags` and no profile fails the
// gate, so a new taggable resource cannot ship without running the matrix.
var tagProfiles = tftagstest.Profiles

type tagProfile = tftagstest.Profile

// TestDefaultTagsContract is the gate: every registered resource with `tags`
// has a Computed `tags_all`, and — through the real protocol server, with a
// fake backend that applies tags the way the platform does (a map REPLACE that
// keeps platform-owned keys, a stamped tenant default on create) — the
// provider's default_tags reach its writes, a resource key wins, keys the
// provider does not manage survive, a removed default is cleared, a
// server-added key lands in tags_all only, an import plans clean, and a clear
// actually clears.
func TestDefaultTagsContract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	taggable := map[string]bool{}
	settings := map[string]bool{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var md resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &md)
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		typeName := md.TypeName

		if _, hasTags := sr.Schema.Attributes["tags"]; !hasTags {
			if _, ok := sr.Schema.Attributes["tags_all"]; ok {
				t.Errorf("%s: has tags_all but no tags", typeName)
			}
			continue
		}
		if _, isSetting := tagsIsASetting[typeName]; isSetting {
			settings[typeName] = true
			if _, ok := sr.Schema.Attributes["tags_all"]; ok {
				t.Errorf("%s: listed in tagsIsASetting but has tags_all — it is either taggable (remove the "+
					"entry and give it a tagProfile) or a setting (remove tags_all)", typeName)
			}
			continue
		}
		taggable[typeName] = true

		t.Run(typeName, func(t *testing.T) {
			t.Parallel()
			tagsAll, ok := sr.Schema.Attributes["tags_all"].(rschema.MapAttribute)
			if !ok {
				t.Fatalf("has `tags` but no map `tags_all` — add tftags.TagsAllAttribute()")
			}
			if !tagsAll.Computed || tagsAll.Optional || tagsAll.Required || !tagsAll.ElementType.Equal(types.StringType) {
				t.Fatalf("tags_all must be a Computed-only map(string), got %+v", tagsAll)
			}
			if _, ok := r.(resource.ResourceWithModifyPlan); !ok {
				t.Fatal("has `tags` but no ModifyPlan — call tftags.PlanTagsAll from one, or a default_tags change never plans")
			}
			if reason, excluded := defaultTagsExclusions[typeName]; excluded {
				t.Skipf("excluded from default_tags: %s", reason)
			}
			prof, ok := tagProfiles[typeName]
			if !ok {
				t.Fatalf("no tagProfile — add one so the default_tags matrix runs against this resource " +
					"(or, with a documented reason, list it in defaultTagsExclusions)")
			}
			runDefaultTagsMatrix(t, typeName, sr.Schema, prof)
		})
	}

	for typeName := range tagProfiles {
		if !taggable[typeName] {
			t.Errorf("tagProfiles lists %s, which is not a registered resource with `tags`", typeName)
		}
	}
	for typeName := range tagsIsASetting {
		if !settings[typeName] {
			t.Errorf("tagsIsASetting lists %s, which is not a registered resource with `tags`", typeName)
		}
		if _, both := tagProfiles[typeName]; both {
			t.Errorf("%s is both profiled and listed as a setting", typeName)
		}
	}
	for typeName := range defaultTagsExclusions {
		if !taggable[typeName] {
			t.Errorf("defaultTagsExclusions lists %s, which is not a registered resource with `tags`", typeName)
		}
		if _, both := tagProfiles[typeName]; both {
			t.Errorf("%s is both profiled and excluded", typeName)
		}
	}
}

func runDefaultTagsMatrix(t *testing.T, typeName string, s rschema.Schema, prof tagProfile) {
	d1 := map[string]string{"env": "prod", "team": "ops", "cost": "42"}
	d2 := map[string]string{"env": "staging", "team": "ops"} // cost removed, env changed
	own := map[string]string{"team": "web", "app": "a"}      // team overrides the default

	h := newTagHarness(t, typeName, s, prof)
	h.fake.Stamp = map[string]string{"stamped": "s"}
	srv1 := h.server(d1)

	// --- create (through the operation-polling path where the real create is
	// async): defaults merged, a resource key wins, a key the platform stamps
	// lands in tags_all only.
	cfg := h.createConfig(own)
	planned, plannedPriv, _ := h.plan(srv1, h.null(), cfg, nil)
	if v := attrOf(planned, "tags_all"); v.IsKnown() {
		t.Errorf("create: planned tags_all = %v, want unknown (the platform may add keys)", v)
	}
	st, priv := h.apply(srv1, h.null(), planned, cfg, plannedPriv)
	wantCreate := map[string]string{"env": "prod", "team": "web", "cost": "42", "app": "a"}
	h.assertLastWrite(http.MethodPost, prof.Field(prof.CreateField), wantCreate, "create")
	h.assertTags(st, own, "create")
	h.assertTagsAll(st, tftagstest.Merge(wantCreate, h.fake.Stamp), "create")
	h.assertConforms(planned, st, "create")
	if prof.Async && h.fake.OperationPolls() == 0 {
		t.Error("create: the real create is async, but the operation was never polled")
	}
	h.converge(srv1, st, priv, own, "create")

	// --- a key added outside Terraform lands in tags_all, never in tags, and is not fought.
	h.fake.Set("portal", "p")
	st, priv = h.read(srv1, st, priv)
	h.assertTags(st, own, "out-of-band refresh")
	if got := mapOf(t, attrOf(st, "tags_all")); got["portal"] != "p" {
		t.Errorf("out-of-band refresh: tags_all = %v, want the portal key", got)
	}
	h.converge(srv1, st, priv, own, "out-of-band refresh")

	// --- a key added between the plan and the apply survives a tags change:
	// the write reads the current tags itself instead of trusting state.
	own = map[string]string{"team": "web", "app": "b"}
	st, priv = h.applyChange(srv1, st, priv, h.configFrom(st, own), "tags change", func() { h.fake.Set("late", "l") })
	h.assertLastWrite(prof.Update, prof.Field(prof.UpdateField),
		map[string]string{"env": "prod", "team": "web", "cost": "42", "app": "b", "stamped": "s", "portal": "p", "late": "l"}, "tags change")
	h.assertTags(st, own, "tags change")
	h.converge(srv1, st, priv, own, "tags change")

	// --- an update for a reason that is not tags plans tags_all unknown, so a
	// key added before its apply is read back without an inconsistent result.
	st, priv = h.applyChange(srv1, st, priv, h.touchConfig(st, own), "update for another reason", func() { h.fake.Set("late2", "x") })
	if got := mapOf(t, attrOf(st, "tags_all")); got["late2"] != "x" {
		t.Errorf("update for another reason: tags_all = %v, want the key added before the apply", got)
	}
	h.converge(srv1, st, priv, own, "update for another reason")

	// --- default_tags change: an in-place update of every taggable resource;
	// unmanaged keys stay, a removed default is cleared.
	srv2 := h.server(d2)
	st, priv = h.applyChange(srv2, st, priv, h.configFrom(st, own), "default_tags change", nil)
	unmanaged := map[string]string{"stamped": "s", "portal": "p", "late": "l", "late2": "x"}
	want := tftagstest.Merge(map[string]string{"env": "staging", "team": "web", "app": "b"}, unmanaged)
	h.assertLastWrite(prof.Update, prof.Field(prof.UpdateField), want, "default_tags change")
	h.assertTags(st, own, "default_tags change")
	h.assertTagsAll(st, want, "default_tags change")
	h.converge(srv2, st, priv, own, "default_tags change")

	// --- a default added by an UPDATE is recorded by that update, so removing
	// it later clears it.
	srv3 := h.server(tftagstest.Merge(d2, map[string]string{"added": "y"}))
	st, priv = h.applyChange(srv3, st, priv, h.configFrom(st, own), "default added", nil)
	h.assertTagsAll(st, tftagstest.Merge(want, map[string]string{"added": "y"}), "default added")
	h.converge(srv3, st, priv, own, "default added")
	st, priv = h.applyChange(srv2, st, priv, h.configFrom(st, own), "added default removed", nil)
	h.assertTagsAll(st, want, "added default removed")
	h.converge(srv2, st, priv, own, "added default removed")

	// --- a recorded default that no write ever cleared is forgotten, so a key
	// of that name added later outside Terraform is not taken for the
	// provider's: (1) a default applied, (2) the key deleted outside Terraform
	// and the default dropped, (3) a refresh, (4) the key added back outside
	// Terraform, (5) the next change keeps it.
	srv4 := h.server(tftagstest.Merge(d2, map[string]string{"gone": "g"}))
	st, priv = h.applyChange(srv4, st, priv, h.configFrom(st, own), "stale default applied", nil)
	h.fake.Delete("gone")
	st, priv = h.read(srv2, st, priv)
	h.converge(srv2, st, priv, own, "stale default dropped")
	h.fake.Set("gone", "portal")
	st, priv = h.read(srv2, st, priv)
	h.converge(srv2, st, priv, own, "same-named key added outside Terraform")
	own = map[string]string{"team": "web", "app": "c"}
	st, _ = h.applyChange(srv2, st, priv, h.configFrom(st, own), "next change", nil)
	if got := mapOf(t, attrOf(st, "tags_all")); got["gone"] != "portal" {
		t.Errorf("next change: tags_all = %v — the portal's key was taken for a stale default and removed", got)
	}
	want = tftagstest.Merge(map[string]string{"env": "staging", "team": "web", "app": "c", "gone": "portal"}, unmanaged)

	// --- import: tags = tags_all minus every pair a default supplies.
	imported, importedPriv := h.importState(srv2)
	wantImported := tftagstest.Merge(map[string]string{"team": "web", "app": "c", "gone": "portal"}, unmanaged)
	h.assertTags(imported, wantImported, "import")

	// A configuration that names every imported key. Its first plan is an
	// update (tags_all unknown) that writes nothing away; that apply is what
	// hands the tags to the configuration, so a removal afterwards removes.
	// Modelled as Terraform runs it: an empty plan is not applied.
	fullCfg := h.configFrom(imported, wantImported)
	firstPlan, firstPriv, _ := h.plan(srv2, imported, fullCfg, importedPriv)
	st, priv = imported, importedPriv
	if changed := changedAttrs(t, imported, firstPlan); len(changed) == 0 {
		t.Error("import, first plan: empty — the import stays pending, and a later removal from tags is kept")
	} else {
		st, priv = h.apply(srv2, imported, firstPlan, fullCfg, firstPriv)
		h.assertConforms(firstPlan, st, "import, first apply")
		h.assertTagsAll(st, want, "import, first apply")
	}
	h.converge(srv2, st, priv, wantImported, "import, after the first apply")

	// Removing a key the configuration names removes it from the resource.
	fewer := tftagstest.Merge(wantImported, nil)
	delete(fewer, "portal")
	st, priv = h.applyChange(srv2, st, priv, h.configFrom(st, fewer), "import, key removed", nil)
	if _, kept := h.fake.UserTags()["portal"]; kept {
		t.Error("import, key removed: the plan removed portal from tags, the apply succeeded, and the key is still on the resource")
	}
	delete(want, "portal")
	h.assertTagsAll(st, want, "import, key removed")
	h.converge(srv2, st, priv, fewer, "import, key removed")

	// A configuration that names only its own keys keeps the rest: the first
	// write after an import never removes a key it does not name.
	reimported, reimportedPriv := h.importState(srv2)
	importOwn := map[string]string{"team": "web", "app": "d"}
	st, priv = h.applyChange(srv2, reimported, reimportedPriv, h.configFrom(reimported, importOwn), "import, own keys only", nil)
	want["app"] = "d"
	h.assertLastWrite(prof.Update, prof.Field(prof.UpdateField), want, "import, own keys only")
	h.assertTags(st, importOwn, "import, own keys only")
	h.assertTagsAll(st, want, "import, own keys only")
	h.converge(srv2, st, priv, importOwn, "import, own keys only")

	// --- clear: with no defaults and no tags, the write empties the set, on every backend spelling.
	c := newTagHarness(t, typeName, s, prof)
	cSrv := c.server(map[string]string{"env": "prod"})
	cCfg := c.createConfig(map[string]string{"app": "a"})
	cPlanned, cPriv, _ := c.plan(cSrv, c.null(), cCfg, nil)
	cst, cstPriv := c.apply(cSrv, c.null(), cPlanned, cCfg, cPriv)
	none := c.server(nil)
	cst, cstPriv = c.applyChange(none, cst, cstPriv, c.configFrom(cst, nil), "clear", nil)
	c.assertLastWriteClears("clear")
	if v := attrOf(cst, "tags"); !v.IsNull() {
		t.Errorf("clear: tags = %v, want null", v)
	}
	c.assertTagsAll(cst, map[string]string{}, "clear")
	if len(c.fake.UserTags()) != 0 {
		t.Errorf("clear: the backend still holds %v — the write did not clear", c.fake.UserTags())
	}
	c.converge(none, cst, cstPriv, nil, "clear")
}

// applyChange plans config over st, requires an in-place update with tags_all
// unknown, runs between (if any) between the plan and the apply — as a change
// made outside Terraform while a saved plan waits — applies, and checks the
// result against the plan the way Terraform core does.
func (h *tagHarness) applyChange(srv tfprotov6.ProviderServer, st tftypes.Value, priv []byte, config tftypes.Value, step string, between func()) (tftypes.Value, []byte) {
	h.t.Helper()
	planned, plannedPriv, replace := h.plan(srv, st, config, priv)
	if len(replace) > 0 {
		h.t.Fatalf("%s: plan requires replacement of %v — must update in place", step, replace)
	}
	if v := attrOf(planned, "tags_all"); v.IsKnown() {
		h.t.Errorf("%s: planned tags_all = %v, want unknown so the update and its read-back show", step, v)
	}
	if between != nil {
		between()
	}
	out, outPriv := h.apply(srv, st, planned, config, plannedPriv)
	h.assertConforms(planned, out, step)
	return out, outPriv
}

// converge re-plans the configuration that matches st, with tags as given, and
// requires an empty plan: Terraform must not want to change anything after an
// apply or a refresh.
func (h *tagHarness) converge(srv tfprotov6.ProviderServer, st tftypes.Value, priv []byte, tags map[string]string, step string) {
	h.t.Helper()
	planned, _, replace := h.plan(srv, st, h.configFromState(st, tags, false), priv)
	if len(replace) > 0 {
		h.t.Errorf("%s: the re-plan requires replacement of %v", step, replace)
	}
	if changed := changedAttrs(h.t, st, planned); len(changed) > 0 {
		h.t.Errorf("%s: the re-plan is not empty: %v", step, changed)
	}
}

// touchConfig is configFrom with the profile's non-tag change applied.
func (h *tagHarness) touchConfig(st tftypes.Value, tags map[string]string) tftypes.Value {
	h.t.Helper()
	if len(h.prof.Touch) == 0 {
		h.t.Fatal("the profile names no Touch attribute to drive an update for another reason")
	}
	vals := objAttrs(h.t, h.configFrom(st, tags))
	out := make(map[string]tftypes.Value, len(vals))
	for k, v := range vals {
		out[k] = v
	}
	for name, v := range h.prof.Touch {
		at, ok := h.objType.AttributeTypes[name]
		if !ok {
			h.t.Fatalf("Touch names %q, not an attribute", name)
		}
		if obj, isObj := at.(tftypes.Object); isObj { // the timeouts block: set its update budget
			attrs := map[string]tftypes.Value{}
			for an, aty := range obj.AttributeTypes {
				attrs[an] = tftypes.NewValue(aty, nil)
			}
			attrs["update"] = tftypes.NewValue(tftypes.String, v)
			out[name] = tftypes.NewValue(obj, attrs)
			continue
		}
		out[name] = tftypes.NewValue(at, v)
	}
	return tftypes.NewValue(h.objType, out)
}

// --- harness ---

type tagHarness struct {
	t        *testing.T
	typeName string
	schema   rschema.Schema
	objType  tftypes.Object
	prof     tagProfile
	fake     *tftagstest.Fake
	httpSrv  *httptest.Server
}

func newTagHarness(t *testing.T, typeName string, s rschema.Schema, prof tagProfile) *tagHarness {
	t.Helper()
	ot, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatalf("%s: schema type is not an object", typeName)
	}
	fake := tftagstest.NewFake(prof)
	h := &tagHarness{t: t, typeName: typeName, schema: s, objType: ot, prof: prof, fake: fake}
	h.httpSrv = httptest.NewServer(fake)
	t.Cleanup(h.httpSrv.Close)
	return h
}

// server returns a provider server configured against the fake with the given
// default_tags (nil: no default_tags block).
func (h *tagHarness) server(defaults map[string]string) tfprotov6.ProviderServer {
	h.t.Helper()
	endpoint, key, noCLI := h.httpSrv.URL, "k", false // pragma: allowlist secret
	v := providerConfigValues{endpoint: &endpoint, apiKey: &key, useCLIConfig: &noCLI}
	if defaults != nil {
		dt := tftypes.NewValue(defaultTagsType, map[string]tftypes.Value{"tags": stringMap(defaults)})
		v.defaultTags = &dt
	}
	srv := providerserver.NewProtocol6(New("test")())()
	resp, err := srv.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{Config: newProviderConfig(h.t, v)})
	if err != nil {
		h.t.Fatalf("ConfigureProvider: %v", err)
	}
	failOnErrors(h.t, "configure", resp.Diagnostics)
	return srv
}

func (h *tagHarness) null() tftypes.Value { return tftypes.NewValue(h.objType, nil) }

// createConfig is tftagstest.CreateConfig for this resource.
func (h *tagHarness) createConfig(tags map[string]string) tftypes.Value {
	return tftagstest.CreateConfig(h.t, h.schema, h.prof, tags)
}

// configFrom is the configuration that matches a state except for tags: every
// configurable attribute takes the state's value, every Computed-only one is
// left to the provider.
func (h *tagHarness) configFrom(state tftypes.Value, tags map[string]string) tftypes.Value {
	return h.configFromState(state, tags, true)
}

// configFromState is configFrom; fillUnread also puts back the profile's
// values for attributes the API never reads back (an imported secret's value),
// as a practitioner's configuration would hold them. converge leaves them out:
// it asks whether the state as it stands plans empty.
func (h *tagHarness) configFromState(state tftypes.Value, tags map[string]string, fillUnread bool) tftypes.Value {
	st := objAttrs(h.t, state)
	vals := map[string]tftypes.Value{}
	for name, at := range h.objType.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
		a, isAttr := h.schema.Attributes[name]
		if isAttr && (a.IsRequired() || a.IsOptional()) && !a.IsWriteOnly() {
			vals[name] = st[name]
			// A value the API never reads back (an imported secret's value) is
			// still in a practitioner's configuration.
			if v, configured := h.prof.Config[name]; fillUnread && configured && st[name].IsNull() {
				vals[name] = v
			}
		}
		if _, isBlock := h.schema.Blocks[name]; isBlock { // timeouts: configured, carried as state holds it
			vals[name] = st[name]
		}
	}
	vals["tags"] = tftagstest.Tags(tags)
	return tftypes.NewValue(h.objType, vals)
}

// proposed is Terraform core's proposed new state, to the extent this matrix
// needs it: the configuration, with a null Computed attribute taking its prior
// value.
func (h *tagHarness) proposed(prior, config tftypes.Value) tftypes.Value {
	if prior.IsNull() {
		return config
	}
	pr, cf := objAttrs(h.t, prior), objAttrs(h.t, config)
	out := map[string]tftypes.Value{}
	for name := range h.objType.AttributeTypes {
		out[name] = cf[name]
		if a, ok := h.schema.Attributes[name]; ok && a.IsComputed() && cf[name].IsNull() {
			out[name] = pr[name]
		}
	}
	return tftypes.NewValue(h.objType, out)
}

func (h *tagHarness) dyn(v tftypes.Value) *tfprotov6.DynamicValue {
	h.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(h.objType, v)
	if err != nil {
		h.t.Fatalf("encode: %v", err)
	}
	return &dv
}

func (h *tagHarness) decode(dv *tfprotov6.DynamicValue) tftypes.Value {
	h.t.Helper()
	if dv == nil {
		h.t.Fatal("no state returned")
	}
	v, err := dv.Unmarshal(h.objType)
	if err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	return v
}

func (h *tagHarness) plan(srv tfprotov6.ProviderServer, prior, config tftypes.Value, private []byte) (tftypes.Value, []byte, []*tftypes.AttributePath) {
	h.t.Helper()
	resp, err := srv.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         h.typeName,
		PriorState:       h.dyn(prior),
		ProposedNewState: h.dyn(h.proposed(prior, config)),
		Config:           h.dyn(config),
		PriorPrivate:     private,
	})
	if err != nil {
		h.t.Fatalf("PlanResourceChange: %v", err)
	}
	failOnErrors(h.t, "plan", resp.Diagnostics)
	return h.decode(resp.PlannedState), resp.PlannedPrivate, resp.RequiresReplace
}

func (h *tagHarness) apply(srv tfprotov6.ProviderServer, prior, planned, config tftypes.Value, private []byte) (tftypes.Value, []byte) {
	h.t.Helper()
	resp, err := srv.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       h.typeName,
		PriorState:     h.dyn(prior),
		PlannedState:   h.dyn(planned),
		Config:         h.dyn(config),
		PlannedPrivate: private,
	})
	if err != nil {
		h.t.Fatalf("ApplyResourceChange: %v", err)
	}
	failOnErrors(h.t, "apply", resp.Diagnostics)
	return h.decode(resp.NewState), resp.Private
}

func (h *tagHarness) read(srv tfprotov6.ProviderServer, state tftypes.Value, private []byte) (tftypes.Value, []byte) {
	h.t.Helper()
	resp, err := srv.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName: h.typeName, CurrentState: h.dyn(state), Private: private,
	})
	if err != nil {
		h.t.Fatalf("ReadResource: %v", err)
	}
	failOnErrors(h.t, "read", resp.Diagnostics)
	st := h.decode(resp.NewState)
	if st.IsNull() {
		h.t.Fatal("read: the refresh removed the resource — the fake answered 404 for the path its Read asked for")
	}
	return st, resp.Private
}

func (h *tagHarness) importState(srv tfprotov6.ProviderServer) (tftypes.Value, []byte) {
	h.t.Helper()
	id := h.prof.ImportID
	if id == "" {
		id = "obj-1"
	}
	resp, err := srv.ImportResourceState(context.Background(), &tfprotov6.ImportResourceStateRequest{TypeName: h.typeName, ID: id})
	if err != nil {
		h.t.Fatalf("ImportResourceState: %v", err)
	}
	failOnErrors(h.t, "import", resp.Diagnostics)
	if len(resp.ImportedResources) != 1 {
		h.t.Fatalf("import returned %d resources", len(resp.ImportedResources))
	}
	imp := resp.ImportedResources[0]
	return h.read(srv, h.decode(imp.State), imp.Private)
}

func (h *tagHarness) assertTags(state tftypes.Value, want map[string]string, step string) {
	h.t.Helper()
	got := mapOf(h.t, attrOf(state, "tags"))
	if !tftagstest.Equal(got, want) {
		h.t.Errorf("%s: tags = %v, want exactly what the configuration names: %v", step, got, want)
	}
}

func (h *tagHarness) assertTagsAll(state tftypes.Value, want map[string]string, step string) {
	h.t.Helper()
	v := attrOf(state, "tags_all")
	if !v.IsKnown() || v.IsNull() {
		h.t.Fatalf("%s: tags_all = %v, want a known map", step, v)
	}
	got := mapOf(h.t, v)
	for k := range h.prof.Reserved {
		if _, leaked := got[k]; leaked {
			h.t.Errorf("%s: platform-owned key %q reached tags_all", step, k)
		}
	}
	if !tftagstest.Equal(got, want) {
		h.t.Errorf("%s: tags_all = %v, want %v", step, got, want)
	}
}

// assertConforms is Terraform core's post-apply check, for the two tag
// attributes: a known planned value must be exactly what the apply returns, or
// the apply fails "Provider produced inconsistent result after apply".
func (h *tagHarness) assertConforms(planned, applied tftypes.Value, step string) {
	h.t.Helper()
	for _, name := range []string{"tags", "tags_all"} {
		p := attrOf(planned, name)
		if p.IsFullyKnown() && !p.Equal(attrOf(applied, name)) {
			h.t.Errorf("%s: planned %s = %v but the apply returned %v (inconsistent result after apply)", step, name, p, attrOf(applied, name))
		}
	}
}

func (h *tagHarness) assertLastWrite(method, field string, want map[string]string, step string) {
	h.t.Helper()
	w := h.fake.LastWrite(method)
	if w == nil {
		h.t.Fatalf("%s: no %s reached the backend", step, method)
	}
	got, ok := w.SentTags(h.t, field)
	if !ok {
		h.t.Fatalf("%s: the %s body carried no %q — got %v", step, method, field, w.Body)
	}
	if !tftagstest.Equal(got, want) {
		h.t.Errorf("%s: the %s sent %s = %v, want %v", step, method, field, got, want)
	}
}

// assertLastWriteClears: the update emptied the set in the backend's own
// spelling — an explicit {} for a map-replace, or the clear flag where {}
// cannot cross the wire.
func (h *tagHarness) assertLastWriteClears(step string) {
	h.t.Helper()
	w := h.fake.LastWrite(h.prof.Update)
	if w == nil {
		h.t.Fatalf("%s: no %s reached the backend", step, h.prof.Update)
	}
	if h.prof.ClearFlag != "" {
		if string(w.Body[h.prof.ClearFlag]) != "true" {
			h.t.Errorf("%s: the update did not set %s: %v", step, h.prof.ClearFlag, w.Body)
		}
		return
	}
	field := h.prof.Field(h.prof.UpdateField)
	if raw := w.Body[field]; strings.TrimSpace(string(raw)) != "{}" {
		h.t.Errorf("%s: the update sent %s = %s, want {} (an omitted or null field means KEEP)", step, field, raw)
	}
}

// --- values ---

// changedAttrs lists the attributes whose planned value differs from prior.
func changedAttrs(t *testing.T, prior, planned tftypes.Value) []string {
	t.Helper()
	pr, pl := objAttrs(t, prior), objAttrs(t, planned)
	var out []string
	for name, v := range pl {
		if !v.Equal(pr[name]) {
			out = append(out, name+"="+v.String()+" (was "+pr[name].String()+")")
		}
	}
	sort.Strings(out)
	return out
}

func attrOf(obj tftypes.Value, name string) tftypes.Value {
	var attrs map[string]tftypes.Value
	if err := obj.As(&attrs); err != nil {
		panic(err)
	}
	return attrs[name]
}

func objAttrs(t *testing.T, v tftypes.Value) map[string]tftypes.Value {
	t.Helper()
	var attrs map[string]tftypes.Value
	if err := v.As(&attrs); err != nil {
		t.Fatalf("not an object: %v", err)
	}
	return attrs
}

// mapOf renders a map(string) value; nil for null.
func mapOf(t *testing.T, v tftypes.Value) map[string]string {
	t.Helper()
	if v.IsNull() {
		return nil
	}
	var elems map[string]tftypes.Value
	if err := v.As(&elems); err != nil {
		t.Fatalf("not a map: %v", err)
	}
	out := map[string]string{}
	for k, e := range elems {
		var s string
		if err := e.As(&s); err != nil {
			t.Fatalf("map element %q: %v", k, err)
		}
		out[k] = s
	}
	return out
}

func stringMap(m map[string]string) tftypes.Value {
	elems := map[string]tftypes.Value{}
	for k, v := range m {
		elems[k] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, elems)
}

func failOnErrors(t *testing.T, step string, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	var msgs []string
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			msgs = append(msgs, d.Summary+": "+d.Detail)
		}
	}
	if len(msgs) > 0 {
		sort.Strings(msgs)
		t.Fatalf("%s: error diagnostics:\n%s", step, strings.Join(msgs, "\n"))
	}
}
