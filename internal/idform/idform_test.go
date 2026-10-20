package idform

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
)

const (
	u1 = "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a01"
	u2 = "019a0c3e-7b10-7cc0-8a52-3f1d2e9b6a02"
)

// all treats every path as an id path.
func all(m mode) rules { return rules{m, func(string) bool { return true }} }

func enc(t *testing.T, prefix, id string) string {
	t.Helper()
	s, ok := pubid.Encode(prefix, id)
	if !ok {
		t.Fatalf("encode %s %s", prefix, id)
	}
	return s
}

func TestSameString(t *testing.T) {
	ins1, vpc1, ins2 := enc(t, "ins", u1), enc(t, "vpc", u1), enc(t, "ins", u2)
	img1, isn1 := enc(t, "img", u1), enc(t, "isn", u1)
	nic2 := enc(t, "nic", u2)
	for _, c := range []struct {
		a, b         string
		strict, lose bool
	}{
		{u1, ins1, true, true},
		{ins1, ins1, true, true},
		{ins1, vpc1, false, true}, // same bytes, other kind
		{img1, isn1, true, true},  // input-only alias
		{ins1, ins2, false, false},
		{u1 + "/" + u2, ins1 + "/" + nic2, true, true},
		{u1 + "/" + u2, ins1 + ":" + nic2, false, false}, // other separator
		{u1 + "/x", ins1 + "/y", false, false},           // non-id part differs
		{ins1 + ":3", u1 + ":3", true, true},
		{"name", "name", true, true},
		{"a", "b", false, false},
		{u1 + "/x", ins1, false, false},           // a different number of parts
		{strings.ToUpper(u1), ins1, false, false}, // an upper-case UUID is text
		{strings.ToUpper(u1), u1, false, false},
	} {
		if got := sameString(c.a, c.b, strict); got != c.strict {
			t.Errorf("strict sameString(%q, %q) = %v", c.a, c.b, got)
		}
		if got := sameString(c.a, c.b, loose); got != c.lose {
			t.Errorf("loose sameString(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestApplyTemplate(t *testing.T) {
	ins1, nic2 := enc(t, "ins", u1), enc(t, "nic", u2)
	mq1, agw1, agl2 := enc(t, "mq", u1), enc(t, "agw", u1), enc(t, "agl", u2)
	vpc1 := enc(t, "vpc", u1)
	for _, c := range []struct{ in, tmpl, want string }{
		{u1, "ins", ins1},
		{ins1, "ins", ins1}, // idempotent
		{"not-an-id", "ins", "not-an-id"},
		{u1 + "/" + u2, "ins/nic", ins1 + "/" + nic2},
		{u1 + "/" + u2, "ins/-", ins1 + "/" + u2},
		{u1 + ":7", "agw:-", agw1 + ":7"},
		{u1 + "/7", "agw:-", u1 + "/7"}, // other shape: untouched
		{u1, "agw|agw/agl", agw1},
		{u1 + "/" + u2, "agw|agw/agl", agw1 + "/" + agl2},
		{u1 + "/alice//", "mq/*", mq1 + "/alice//"},
		{u1 + "/2001:db8::/32", "vpc/*", vpc1 + "/2001:db8::/32"},
		{u1 + "/" + u2, "zzz/ins", u1 + "/" + enc(t, "ins", u2)}, // unregistered prefix: that part stays
	} {
		if got := applyTemplate(c.in, c.tmpl); got != c.want {
			t.Errorf("applyTemplate(%q, %q) = %q, want %q", c.in, c.tmpl, got, c.want)
		}
	}
}

var objType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
	"id":     tftypes.String,
	"vpc_id": tftypes.String,
	"name":   tftypes.String,
	"sg_ids": tftypes.Set{ElementType: tftypes.String},
	"rules": tftypes.List{ElementType: tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"id": tftypes.String,
	}}},
}}

func obj(id, vpc, name string, sgs []string, rules []string) tftypes.Value {
	set := make([]tftypes.Value, len(sgs))
	for i, s := range sgs {
		set[i] = tftypes.NewValue(tftypes.String, s)
	}
	ruleType := objType.AttributeTypes["rules"].(tftypes.List).ElementType
	rl := make([]tftypes.Value, len(rules))
	for i, r := range rules {
		rl[i] = tftypes.NewValue(ruleType, map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, r)})
	}
	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"id":     tftypes.NewValue(tftypes.String, id),
		"vpc_id": tftypes.NewValue(tftypes.String, vpc),
		"name":   tftypes.NewValue(tftypes.String, name),
		"sg_ids": tftypes.NewValue(objType.AttributeTypes["sg_ids"], set),
		"rules":  tftypes.NewValue(objType.AttributeTypes["rules"], rl),
	})
}

// Each element of one set pairs with a distinct element of the other: a set
// holding two spellings of one id is not the same as one holding that id once
// plus another value.
func TestSameSetPairsDistinctElements(t *testing.T) {
	st := tftypes.Set{ElementType: tftypes.String}
	set := func(xs ...string) tftypes.Value {
		vs := make([]tftypes.Value, len(xs))
		for i, x := range xs {
			vs[i] = tftypes.NewValue(tftypes.String, x)
		}
		return tftypes.NewValue(st, vs)
	}
	ins1 := enc(t, "ins", u1)
	if same(set(u1, ins1), set(ins1, "x"), all(strict), "ids") {
		t.Error("two spellings of one id matched one id and another value")
	}
	if !same(set(u1, u2), set(enc(t, "ins", u2), ins1), all(strict), "ids") {
		t.Error("a set of UUIDs is not the same as its public spellings in another order")
	}
}

func TestRewrite(t *testing.T) {
	kinds := map[string]string{"id": "ins", "vpc_id": "vpc", "sg_ids": "sg", "rules.id": "sgr"}
	got := rewrite(obj(u1, u2, u1, []string{u1, u2}, []string{u2}), kinds)
	want := obj(enc(t, "ins", u1), enc(t, "vpc", u2), u1, []string{enc(t, "sg", u1), enc(t, "sg", u2)}, []string{enc(t, "sgr", u2)})
	if !got.Equal(want) {
		t.Errorf("rewrite = %v\nwant %v", got, want)
	}
	if again := rewrite(got, kinds); !again.Equal(want) {
		t.Errorf("rewrite is not idempotent: %v", again)
	}
}

func TestKeepPrior(t *testing.T) {
	ins1, vpc2, sg1, sg2 := enc(t, "ins", u1), enc(t, "vpc", u2), enc(t, "sg", u1), enc(t, "sg", u2)

	// The API echoes public ids for a plan written with UUIDs: the plan's
	// spelling is kept everywhere, sets included.
	plan := obj(u1, u2, "n", []string{u2, u1}, []string{u1})
	api := obj(ins1, vpc2, "n", []string{sg1, sg2}, []string{enc(t, "sgr", u1)})
	if got := keepPrior(api, plan, all(loose), nil); !got.Equal(plan) {
		t.Errorf("apply keep = %v\nwant %v", got, plan)
	}

	// A real change is never masked.
	changed := obj(ins1, enc(t, "vpc", u1), "renamed", []string{sg1}, nil)
	got := keepPrior(changed, plan, all(loose), nil)
	if want := obj(u1, enc(t, "vpc", u1), "renamed", []string{u1}, nil); !got.Equal(want) {
		t.Errorf("keep over a change = %v\nwant %v", got, want)
	}

	// Read: a computed-only path takes the API's spelling.
	if got := keepPrior(api, plan, all(strict), map[string]bool{"id": true}); !got.Equal(obj(ins1, u2, "n", []string{u2, u1}, []string{u1})) {
		t.Errorf("read keep with computed-only id = %v", got)
	}

	// Only id paths compare as ids: a name holding a UUID is text.
	byName := rules{loose, func(p string) bool { return p == "id" }}
	if got := keepPrior(obj(ins1, u2, ins1, nil, nil), obj(u1, u2, u1, nil, nil), byName, nil); !got.Equal(obj(u1, u2, ins1, nil, nil)) {
		t.Errorf("keep at a non-id path: %v", got)
	}
	if same(obj(u1, u2, u1, nil, nil), obj(u1, u2, ins1, nil, nil), rules{strict, idName}, "") {
		t.Error("a UUID-shaped name compared as an id")
	}

	// Read, strict: a prior public id of another kind gives way to the API.
	prior := obj(enc(t, "sub", u1), u2, "n", nil, nil)
	if got := keepPrior(obj(ins1, u2, "n", nil, nil), prior, all(strict), nil); !got.Equal(obj(ins1, u2, "n", nil, nil)) {
		t.Errorf("strict keep kept a wrong-kind prior: %v", got)
	}
}

// fakeResource records whether Update reached it and echoes public ids.
type fakeResource struct {
	updated bool
	mods    []planmodifier.String
}

func (f *fakeResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "frostmoln_instance"
}

func (f *fakeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"id":     schema.StringAttribute{Computed: true},
		"vpc_id": schema.StringAttribute{Required: true, PlanModifiers: f.mods},
		"name":   schema.StringAttribute{Required: true},
	}}
}

func (f *fakeResource) Create(context.Context, resource.CreateRequest, *resource.CreateResponse) {}
func (f *fakeResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse)       {}
func (f *fakeResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}
func (f *fakeResource) Update(_ context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	f.updated = true
	resp.State.Raw = req.Plan.Raw
}

func state(t *testing.T, w resource.Resource, id, vpc, name string) tfsdk.State {
	t.Helper()
	var sr resource.SchemaResponse
	w.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(context.Background())
	return tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.String, id), "vpc_id": tftypes.NewValue(tftypes.String, vpc),
		"name": tftypes.NewValue(tftypes.String, name),
	})}
}

func TestUpdateShortCircuitsAFormOnlyDiff(t *testing.T) {
	ctx := context.Background()
	inner := &fakeResource{}
	w := Wrap(inner)
	st := state(t, w, enc(t, "ins", u1), enc(t, "vpc", u2), "n")
	plan := state(t, w, enc(t, "ins", u1), u2, "n")
	resp := resource.UpdateResponse{State: st}
	w.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan(plan), State: st}, &resp)
	if inner.updated {
		t.Error("a form-only diff reached the API")
	}
	if !resp.State.Raw.Equal(plan.Raw) {
		t.Errorf("state = %v, want the plan", resp.State.Raw)
	}

	for name, plan := range map[string]tfsdk.State{
		"a real change":                state(t, w, enc(t, "ins", u1), u2, "renamed"),
		"the same bytes, another kind": state(t, w, enc(t, "ins", u1), enc(t, "sub", u2), "n"),
	} {
		inner.updated = false
		resp = resource.UpdateResponse{State: st}
		w.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan(plan), State: st}, &resp)
		if !inner.updated {
			t.Errorf("%s did not reach the API", name)
		}
	}

	// An unknown planned value is a change the apply computes.
	unknown := state(t, w, enc(t, "ins", u1), u2, "n")
	unknown.Raw = tftypes.NewValue(unknown.Raw.Type(), map[string]tftypes.Value{
		"id":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"vpc_id": tftypes.NewValue(tftypes.String, u2), "name": tftypes.NewValue(tftypes.String, "n"),
	})
	inner.updated = false
	resp = resource.UpdateResponse{State: st}
	w.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan(unknown), State: st}, &resp)
	if !inner.updated {
		t.Error("a plan with an unknown value did not reach the API")
	}
}

func TestReplaceGuard(t *testing.T) {
	ctx := context.Background()
	w := Wrap(&fakeResource{mods: []planmodifier.String{stringplanmodifier.RequiresReplace()}})
	var sr resource.SchemaResponse
	w.Schema(ctx, resource.SchemaRequest{}, &sr)
	if sr.Schema.Version != 1 {
		t.Errorf("schema version = %d, want 1", sr.Schema.Version)
	}
	mod := sr.Schema.Attributes["vpc_id"].(schema.StringAttribute).PlanModifiers[0]
	for _, c := range []struct {
		state, plan string
		replace     bool
	}{
		{enc(t, "vpc", u1), u1, false},
		{u1, enc(t, "vpc", u1), false},
		{enc(t, "vpc", u1), enc(t, "vpc", u2), true},
		{enc(t, "vpc", u1), enc(t, "sub", u1), true}, // same bytes, other kind
		{u1, u2, true},
	} {
		req := planmodifier.StringRequest{
			StateValue: types.StringValue(c.state), PlanValue: types.StringValue(c.plan), ConfigValue: types.StringValue(c.plan),
			State: tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
			Plan:  tfsdk.Plan{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
		}
		resp := planmodifier.StringResponse{PlanValue: req.PlanValue}
		mod.PlanModifyString(ctx, req, &resp)
		if resp.RequiresReplace != c.replace {
			t.Errorf("state %s plan %s: replace = %v, want %v", c.state, c.plan, resp.RequiresReplace, c.replace)
		}
	}
}

// upgradingResource has an older v0 -> v1 upgrader of its own.
type upgradingResource struct{ fakeResource }

func (u *upgradingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	u.fakeResource.Schema(ctx, req, resp)
	resp.Schema.Version = 1
}

func (u *upgradingResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{0: {StateUpgrader: func(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
		// v0 had no name.
		resp.DynamicValue = &tfprotov6.DynamicValue{JSON: append(req.RawState.JSON[:len(req.RawState.JSON)-1], []byte(`,"name":"from-v0"}`)...)}
	}}}
}

func TestUpgraders(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		r       resource.Resource
		from    int64
		raw     string
		version int64
	}{
		{"single version", &fakeResource{}, 0, `{"id":"` + u1 + `","vpc_id":"` + u2 + `","name":"n"}`, 1},
		{"after its own upgrader", &upgradingResource{}, 0, `{"id":"` + u1 + `","vpc_id":"` + u2 + `"}`, 2},
		{"current version", &upgradingResource{}, 1, `{"id":"` + u1 + `","vpc_id":"` + u2 + `","name":"n"}`, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := Wrap(c.r)
			var sr resource.SchemaResponse
			w.Schema(ctx, resource.SchemaRequest{}, &sr)
			if sr.Schema.Version != c.version {
				t.Fatalf("version = %d, want %d", sr.Schema.Version, c.version)
			}
			ups := w.(resource.ResourceWithUpgradeState).UpgradeState(ctx)
			u, ok := ups[c.from]
			if !ok {
				t.Fatalf("no upgrader from v%d (have %v)", c.from, len(ups))
			}
			resp := resource.UpgradeStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
			u.StateUpgrader(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte(c.raw)}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var m map[string]tftypes.Value
			if err := resp.State.Raw.As(&m); err != nil {
				t.Fatal(err)
			}
			var id, vpc string
			_ = m["id"].As(&id)
			_ = m["vpc_id"].As(&vpc)
			if id != enc(t, "ins", u1) || vpc != enc(t, "vpc", u2) {
				t.Errorf("upgraded id=%s vpc_id=%s", id, vpc)
			}
		})
	}
}

type fakeDataSource struct{ answer string }

func (f *fakeDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "frostmoln_vpc"
}

func (f *fakeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{Attributes: map[string]dsschema.Attribute{
		"id": dsschema.StringAttribute{Optional: true, Computed: true},
	}}
}

func (f *fakeDataSource) Read(_ context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	resp.State.Raw = tftypes.NewValue(req.Config.Raw.Type(), map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, f.answer)})
}

// A data source configured with a UUID keeps it, though the API answers with
// the public id; a different id is never masked.
func TestDataSourceKeepsTheConfiguredSpelling(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ answer, want string }{
		{enc(t, "vpc", u1), u1},
		{enc(t, "vpc", u2), enc(t, "vpc", u2)},
	} {
		d := WrapDataSource(&fakeDataSource{answer: c.answer})
		var sr datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sr)
		typ := sr.Schema.Type().TerraformType(ctx)
		cfg := tftypes.NewValue(typ, map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, u1)})
		resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg}}, &resp)
		var m map[string]tftypes.Value
		_ = resp.State.Raw.As(&m)
		var got string
		_ = m["id"].As(&got)
		if got != c.want {
			t.Errorf("API %s: id = %s, want %s", c.answer, got, c.want)
		}
	}
}

// Only id paths are guarded: a replace-forcing attribute that is not an id
// keeps its replacement however id-like its values look, and a kind-map path
// whose name does not say "id" (frostmoln_instance security_groups) counts.
func TestReplaceGuardOnlyAtIDPaths(t *testing.T) {
	ctx := context.Background()
	w := Wrap(&labelResource{})
	var sr resource.SchemaResponse
	w.Schema(ctx, resource.SchemaRequest{}, &sr)
	mod := sr.Schema.Attributes["label"].(schema.StringAttribute).PlanModifiers[0]
	req := planmodifier.StringRequest{
		StateValue: types.StringValue(u1), PlanValue: types.StringValue(enc(t, "ins", u1)), ConfigValue: types.StringValue(enc(t, "ins", u1)),
		State: tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
		Plan:  tfsdk.Plan{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
	}
	resp := planmodifier.StringResponse{PlanValue: req.PlanValue}
	mod.PlanModifyString(ctx, req, &resp)
	if !resp.RequiresReplace {
		t.Error("a non-id attribute lost its replacement")
	}
	if !w.(*wrapped).isID("security_groups") {
		t.Error("a kind-map path is not an id path")
	}
	if w.(*wrapped).isID("label") {
		t.Error("label is an id path")
	}
}

type labelResource struct{ fakeResource }

func (l *labelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"id":    schema.StringAttribute{Computed: true},
		"label": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
	}}
}

// echoResource's Read answers with public ids, as the API does.
type echoResource struct{ fakeResource }

func (e *echoResource) Read(_ context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m map[string]tftypes.Value
	_ = req.State.Raw.As(&m)
	out := map[string]tftypes.Value{}
	for k, v := range m {
		var s string
		_ = v.As(&s)
		if p, ok := map[string]string{"id": "ins", "vpc_id": "vpc"}[k]; ok {
			if enc, ok := pubid.Encode(p, s); ok {
				s = enc
			}
		}
		out[k] = tftypes.NewValue(tftypes.String, s)
	}
	resp.State.Raw = tftypes.NewValue(req.State.Raw.Type(), out)
}

// Read keeps a configured UUID (vpc_id) the API now spells publicly, and takes
// the API's spelling of a computed-only id.
func TestReadKeepsPriorSpellingExceptComputedOnly(t *testing.T) {
	ctx := context.Background()
	w := Wrap(&echoResource{})
	st := state(t, w, u1, u2, "n")
	resp := resource.ReadResponse{State: st}
	w.Read(ctx, resource.ReadRequest{State: st}, &resp)
	want := state(t, w, enc(t, "ins", u1), u2, "n")
	if !resp.State.Raw.Equal(want.Raw) {
		t.Errorf("read = %v\nwant %v", resp.State.Raw, want.Raw)
	}
}
