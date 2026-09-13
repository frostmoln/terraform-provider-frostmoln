package tftags

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakePrivate stands in for the framework's private state, which cannot be
// constructed outside the framework.
type fakePrivate map[string][]byte

func (f fakePrivate) GetKey(_ context.Context, k string) ([]byte, diag.Diagnostics) {
	return f[k], nil
}

func (f fakePrivate) SetKey(_ context.Context, k string, v []byte) diag.Diagnostics {
	if len(v) == 0 {
		delete(f, k)
		return nil
	}
	if !json.Valid(v) {
		var d diag.Diagnostics
		d.AddError("invalid JSON", string(v))
		return d
	}
	f[k] = v
	return nil
}

func tm(kv ...string) types.Map {
	m := map[string]attr.Value{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, m)
}

func sm(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func mustNoErr(t *testing.T, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d)
	}
}

func assertMap(t *testing.T, got, want map[string]string) {
	t.Helper()
	if !Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDesiredCreate(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	defaults := Defaults{Tags: sm("env", "prod", "team", "ops")}

	// Defaults only.
	got, ok := Desired(ctx, defaults, types.MapNull(types.StringType), nil, &d)
	mustNoErr(t, d)
	if !ok {
		t.Fatal("not computable")
	}
	assertMap(t, got, sm("env", "prod", "team", "ops"))

	// A resource key wins over a default.
	got, _ = Desired(ctx, defaults, tm("env", "dev", "app", "web"), nil, &d)
	assertMap(t, got, sm("env", "dev", "team", "ops", "app", "web"))

	// Not computable while either side is unknown.
	if _, ok := Desired(ctx, Defaults{Unknown: true}, tm("a", "b"), nil, &d); ok {
		t.Error("unknown defaults must not be computable")
	}
	if _, ok := Desired(ctx, defaults, types.MapUnknown(types.StringType), nil, &d); ok {
		t.Error("unknown tags must not be computable")
	}
	partial := types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringUnknown()})
	if _, ok := Desired(ctx, defaults, partial, nil, &d); ok {
		t.Error("a tag whose value is unknown must not be computable")
	}
}

func TestForCreate(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	if got := ForCreate(ctx, Defaults{}, types.MapNull(types.StringType), &d); got != nil {
		t.Errorf("no tags and no defaults must keep the create's wire shape (nil), got %v", got)
	}
	assertMap(t, ForCreate(ctx, Defaults{Tags: sm("env", "prod")}, types.MapNull(types.StringType), &d), sm("env", "prod"))
	mustNoErr(t, d)

	ForCreate(ctx, Defaults{Unknown: true}, types.MapNull(types.StringType), &d)
	if !d.HasError() {
		t.Error("a write with unknown defaults must fail rather than guess")
	}
}

// TestForUpdate is the write contract: resource tags win over defaults, keys the
// provider does not manage survive the map-replace, and keys it stopped
// managing — a removed resource tag, a removed default — are cleared.
func TestForUpdate(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		defaults    Defaults
		tags        types.Map
		prior       Prior
		want        map[string]string
		wantChanged bool
	}{
		{
			name:        "unmanaged key in tags_all survives a default change",
			defaults:    Defaults{Tags: sm("env", "prod")},
			tags:        tm("app", "web"),
			prior:       Prior{Tags: tm("app", "web"), TagsAll: tm("app", "web", "env", "dev", "cost-center", "42"), DefaultKeys: []string{"env"}},
			want:        sm("app", "web", "env", "prod", "cost-center", "42"),
			wantChanged: true,
		},
		{
			name:        "removed default is cleared",
			defaults:    Defaults{},
			tags:        tm("app", "web"),
			prior:       Prior{Tags: tm("app", "web"), TagsAll: tm("app", "web", "env", "prod"), DefaultKeys: []string{"env"}},
			want:        sm("app", "web"),
			wantChanged: true,
		},
		{
			name:        "removed resource tag is cleared, unmanaged survives",
			defaults:    Defaults{},
			tags:        types.MapNull(types.StringType),
			prior:       Prior{Tags: tm("app", "web"), TagsAll: tm("app", "web", "tenant", "t1")},
			want:        sm("tenant", "t1"),
			wantChanged: true,
		},
		{
			name:        "everything cleared is an empty, non-nil map",
			defaults:    Defaults{},
			tags:        types.MapNull(types.StringType),
			prior:       Prior{Tags: tm("app", "web"), TagsAll: tm("app", "web")},
			want:        sm(),
			wantChanged: true,
		},
		{
			name:        "resource key overrides a default",
			defaults:    Defaults{Tags: sm("env", "prod")},
			tags:        tm("env", "dev"),
			prior:       Prior{Tags: tm("env", "dev"), TagsAll: tm("env", "dev"), DefaultKeys: []string{"env"}},
			want:        sm("env", "dev"),
			wantChanged: false,
		},
		{
			name:        "nothing changed",
			defaults:    Defaults{Tags: sm("env", "prod")},
			tags:        tm("app", "web"),
			prior:       Prior{Tags: tm("app", "web"), TagsAll: tm("app", "web", "env", "prod", "x", "y"), DefaultKeys: []string{"env"}},
			want:        sm("app", "web", "env", "prod", "x", "y"),
			wantChanged: false,
		},
		{
			// State written before tags_all existed: tags held the whole
			// filtered set, every key of it managed.
			name:        "legacy state without tags_all",
			defaults:    Defaults{Tags: sm("env", "prod")},
			tags:        tm("app", "web"),
			prior:       Prior{Tags: tm("app", "web", "old", "1"), TagsAll: types.MapNull(types.StringType)},
			want:        sm("app", "web", "env", "prod"),
			wantChanged: true,
		},
		{
			// The key was unmanaged; a new default claims it.
			name:        "a new default overwrites an unmanaged key",
			defaults:    Defaults{Tags: sm("tenant", "mine")},
			tags:        types.MapNull(types.StringType),
			prior:       Prior{Tags: types.MapNull(types.StringType), TagsAll: tm("tenant", "t1")},
			want:        sm("tenant", "mine"),
			wantChanged: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d diag.Diagnostics
			got, changed := ForUpdate(ctx, tt.defaults, tt.tags, tt.prior, &d)
			mustNoErr(t, d)
			if got == nil {
				t.Fatal("got nil — an omitted field means KEEP, so tags could never be cleared")
			}
			assertMap(t, got, tt.want)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}

	t.Run("unknown tags are no opinion", func(t *testing.T) {
		var d diag.Diagnostics
		got, changed := ForUpdate(ctx, Defaults{}, types.MapUnknown(types.StringType), Prior{}, &d)
		mustNoErr(t, d)
		if got != nil || changed {
			t.Errorf("got %v/%v, want nil/false", got, changed)
		}
	})
	t.Run("unknown defaults fail the write", func(t *testing.T) {
		var d diag.Diagnostics
		ForUpdate(ctx, Defaults{Unknown: true}, tm("a", "b"), Prior{}, &d)
		if !d.HasError() {
			t.Error("a write with unknown defaults must fail rather than guess")
		}
	})
}

func TestReadBack(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		api      map[string]string
		prior    types.Map
		wantTags map[string]string // nil means null
		wantAll  map[string]string
	}{
		// A key only the platform knows (a tenant default, a portal edit) lands
		// in tags_all and never in tags, so it is neither drift nor an
		// inconsistent result.
		{name: "extra key only in tags_all", api: sm("app", "web", "tenant", "t1"), prior: tm("app", "web"), wantTags: sm("app", "web"), wantAll: sm("app", "web", "tenant", "t1")},
		{name: "extra key over null prior", api: sm("tenant", "t1"), prior: types.MapNull(types.StringType), wantTags: nil, wantAll: sm("tenant", "t1")},
		// Drift on a configured key is visible.
		{name: "changed value is drift", api: sm("app", "api"), prior: tm("app", "web"), wantTags: sm("app", "api"), wantAll: sm("app", "api")},
		{name: "removed key drops out", api: nil, prior: tm("app", "web"), wantTags: sm(), wantAll: sm()},
		// `tags = {}` round-trips; an unset attribute stays null.
		{name: "empty prior stays empty", api: nil, prior: tm(), wantTags: sm(), wantAll: sm()},
		{name: "null prior stays null", api: map[string]string{}, prior: types.MapNull(types.StringType), wantTags: nil, wantAll: sm()},
		{name: "unknown prior is null", api: sm("a", "b"), prior: types.MapUnknown(types.StringType), wantTags: nil, wantAll: sm("a", "b")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d diag.Diagnostics
			tags, all := ReadBack(ctx, tt.api, tt.prior, &d)
			mustNoErr(t, d)
			if all.IsNull() || all.IsUnknown() {
				t.Fatalf("tags_all must always be a known map, got %v", all)
			}
			gotAll, _ := knownMap(ctx, all, &d)
			assertMap(t, gotAll, tt.wantAll)
			if tt.wantTags == nil {
				if !tags.IsNull() {
					t.Fatalf("tags = %v, want null", tags)
				}
				return
			}
			if tags.IsNull() || tags.IsUnknown() {
				t.Fatalf("tags = %v, want %v", tags, tt.wantTags)
			}
			gotTags, _ := knownMap(ctx, tags, &d)
			assertMap(t, gotTags, tt.wantTags)
		})
	}
}

func TestPrivateDefaultKeysRoundTrip(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	p := fakePrivate{}
	RecordDefaults(ctx, p, Defaults{Tags: sm("b", "2", "a", "1")}, &d)
	mustNoErr(t, d)
	keys := PriorDefaultKeys(ctx, p, &d)
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys = %v, want [a b]", keys)
	}
	RecordDefaults(ctx, p, Defaults{}, &d)
	if keys := PriorDefaultKeys(ctx, p, &d); keys != nil {
		t.Fatalf("keys = %v after recording no defaults, want none", keys)
	}

	// Direct method calls in tests hand the helpers a nil framework private
	// state; they must treat it as absent, not fail.
	var typedNil *fakePrivateNil
	RecordDefaults(ctx, typedNil, Defaults{Tags: sm("a", "1")}, &d)
	MarkImported(ctx, typedNil, &d)
	if PriorDefaultKeys(ctx, typedNil, &d) != nil {
		t.Error("a nil private state has no keys")
	}
	mustNoErr(t, d)
}

// fakePrivateNil is a pointer type whose nil value the helpers must tolerate,
// as they tolerate the framework's nil *privatestate.ProviderData.
type fakePrivateNil struct{}

func (*fakePrivateNil) GetKey(context.Context, string) ([]byte, diag.Diagnostics) {
	panic("GetKey called on a nil private state")
}

func (*fakePrivateNil) SetKey(context.Context, string, []byte) diag.Diagnostics {
	panic("SetKey called on a nil private state")
}

func TestFinishReadImport(t *testing.T) {
	ctx := context.Background()
	defaults := Defaults{Tags: sm("env", "prod", "team", "ops")}
	all := tm("env", "prod", "team", "platform", "app", "web")

	t.Run("not an import", func(t *testing.T) {
		var d diag.Diagnostics
		p := fakePrivate{}
		tags := tm("app", "web")
		FinishRead(ctx, p, p, defaults, &tags, all, &d)
		mustNoErr(t, d)
		got, _ := knownMap(ctx, tags, &d)
		assertMap(t, got, sm("app", "web"))
	})

	t.Run("import takes tags_all minus the defaults it matches", func(t *testing.T) {
		var d diag.Diagnostics
		p := fakePrivate{}
		MarkImported(ctx, p, &d)
		tags := types.MapNull(types.StringType)
		FinishRead(ctx, p, p, defaults, &tags, all, &d)
		mustNoErr(t, d)
		got, _ := knownMap(ctx, tags, &d)
		// env=prod is the default's value: it stays out. team=platform differs
		// from the default: an override, so it belongs in tags.
		assertMap(t, got, sm("team", "platform", "app", "web"))
		if _, marked := p[privateImported]; marked {
			t.Error("the import marker must be consumed by the first read")
		}
		if keys := PriorDefaultKeys(ctx, p, &d); len(keys) != 2 {
			t.Errorf("the defaults applying to the imported resource must be recorded, got %v", keys)
		}
		if !importPending(ctx, p, &d) {
			t.Error("until the first write the imported tags must not count as managed")
		}
		RecordDefaults(ctx, p, defaults, &d)
		if importPending(ctx, p, &d) {
			t.Error("the first write must end the import's pending state")
		}
	})

	t.Run("an import holding only defaults leaves tags null", func(t *testing.T) {
		var d diag.Diagnostics
		p := fakePrivate{}
		MarkImported(ctx, p, &d)
		tags := types.MapNull(types.StringType)
		FinishRead(ctx, p, p, defaults, &tags, tm("env", "prod"), &d)
		mustNoErr(t, d)
		if !tags.IsNull() {
			t.Errorf("tags = %v, want null so a config without tags plans clean", tags)
		}
	})
}

func testSchema() schema.Schema {
	return schema.Schema{Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true},
		"tags":     schema.MapAttribute{Optional: true, ElementType: types.StringType},
		"tags_all": TagsAllAttribute(),
	}}
}

type testModel struct {
	ID      types.String `tfsdk:"id"`
	Tags    types.Map    `tfsdk:"tags"`
	TagsAll types.Map    `tfsdk:"tags_all"`
}

// plannedTagsAll runs the plan-time half and returns the planned tags_all.
func plannedTagsAll(t *testing.T, d Defaults, state *testModel, plan testModel, private PrivateReader) types.Map {
	t.Helper()
	ctx := context.Background()
	s := testSchema()
	p := tfsdk.Plan{Schema: s}
	mustNoErr(t, p.Set(ctx, &plan))
	// A nil state is a create: the prior state is a null object.
	st := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if state != nil {
		mustNoErr(t, st.Set(ctx, state))
	}
	out := tfsdk.Plan{Schema: s, Raw: p.Raw.Copy()}
	var diags diag.Diagnostics
	planTagsAll(ctx, d, p, st, private, &out, &diags)
	mustNoErr(t, diags)
	var got types.Map
	mustNoErr(t, out.GetAttribute(ctx, path.Root("tags_all"), &got))
	return got
}

func TestPlanTagsAll(t *testing.T) {
	stateModel := func(tags, all types.Map) *testModel {
		return &testModel{ID: types.StringValue("x"), Tags: tags, TagsAll: all}
	}
	planModel := func(tags, all types.Map) testModel {
		return testModel{ID: types.StringValue("x"), Tags: tags, TagsAll: all}
	}
	withKeys := func(keys ...string) fakePrivate {
		p := fakePrivate{}
		raw, _ := json.Marshal(keys)
		p[privateDefaultKeys] = raw
		return p
	}

	all := tm("app", "web", "env", "prod", "tenant", "t1")

	t.Run("create predicts unknown", func(t *testing.T) {
		got := plannedTagsAll(t, Defaults{}, nil, planModel(tm("app", "web"), types.MapUnknown(types.StringType)), nil)
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v on a create, want unknown: the platform may stamp tenant defaults", got)
		}
	})
	t.Run("nothing changed keeps state", func(t *testing.T) {
		got := plannedTagsAll(t, Defaults{Tags: sm("env", "prod")}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "web"), all), withKeys("env"))
		if !got.Equal(all) {
			t.Errorf("tags_all = %v, want the state value %v", got, all)
		}
	})
	t.Run("a changed default plans unknown", func(t *testing.T) {
		got := plannedTagsAll(t, Defaults{Tags: sm("env", "staging")}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "web"), all), withKeys("env"))
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v, want unknown after a default_tags change", got)
		}
	})
	t.Run("a removed default plans unknown", func(t *testing.T) {
		got := plannedTagsAll(t, Defaults{}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "web"), all), withKeys("env"))
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v, want unknown after a default was removed", got)
		}
	})
	t.Run("a changed resource tag plans unknown", func(t *testing.T) {
		got := plannedTagsAll(t, Defaults{Tags: sm("env", "prod")}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "api"), all), withKeys("env"))
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v, want unknown after a tags change", got)
		}
	})
	t.Run("unknown defaults plan unknown", func(t *testing.T) {
		got := plannedTagsAll(t, Defaults{Unknown: true}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "web"), all), withKeys("env"))
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v, want unknown while default_tags are not known", got)
		}
	})
	t.Run("an update for any reason plans unknown", func(t *testing.T) {
		// Same tags, same defaults — but the resource is updated, and its
		// read-back can hold a key added since the last refresh.
		st := stateModel(tm("app", "web"), all)
		pl := planModel(tm("app", "web"), all)
		pl.ID = types.StringValue("renamed")
		got := plannedTagsAll(t, Defaults{Tags: sm("env", "prod")}, st, pl, withKeys("env"))
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v on an update, want unknown", got)
		}
	})
	t.Run("the first plan after an import is an update", func(t *testing.T) {
		// Nothing differs — but the import is pending until a write, so the
		// plan must produce one.
		p := withKeys("env")
		p[privateImportPending] = []byte("true")
		got := plannedTagsAll(t, Defaults{Tags: sm("env", "prod")}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "web"), all), p)
		if !got.IsUnknown() {
			t.Errorf("tags_all = %v while an import is pending, want unknown so the first apply writes", got)
		}
	})
	t.Run("an unmanaged key never plans a change", func(t *testing.T) {
		// tenant=t1 is in no configuration and no default: kept, not fought.
		got := plannedTagsAll(t, Defaults{Tags: sm("env", "prod")}, stateModel(tm("app", "web"), all),
			planModel(tm("app", "web"), all), withKeys("env"))
		if got.IsUnknown() {
			t.Error("an unmanaged key made the plan predict a tag change")
		}
	})
}

// TestUnmanagedAfterImport: until the first write, the tags an import read
// back are not the configuration's, so a key the configuration does not name
// is kept — as it would be on a resource Terraform created.
func TestUnmanagedAfterImport(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	imported := tm("app", "a", "stamped", "s", "portal", "p")
	prior := Prior{Tags: imported, TagsAll: imported, ImportPending: true}
	got, changed := ForUpdate(ctx, Defaults{}, tm("app", "b"), prior, &d)
	mustNoErr(t, d)
	assertMap(t, got, sm("app", "b", "stamped", "s", "portal", "p"))
	if !changed {
		t.Error("the app change must be written")
	}

	// The same prior after a write: the tags are the configuration's, and a
	// key dropped from it is cleared.
	prior.ImportPending = false
	got, _ = ForUpdate(ctx, Defaults{}, tm("app", "b"), prior, &d)
	assertMap(t, got, sm("app", "b"))
}

// TestWithCurrent: the keys to keep come from the platform's set as read right
// before the write, not from state — a key added between plan and apply
// survives, and whether anything changed is judged against the fresh set.
func TestWithCurrent(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	state := tm("app", "a", "env", "prod")
	prior := Prior{Tags: tm("app", "a"), TagsAll: state, DefaultKeys: []string{"env"}}

	got, changed := ForUpdate(ctx, Defaults{Tags: sm("env", "prod")}, tm("app", "a"), prior.WithCurrent(sm("app", "a", "env", "prod", "late", "x")), &d)
	mustNoErr(t, d)
	assertMap(t, got, sm("app", "a", "env", "prod", "late", "x"))
	if changed {
		t.Error("nothing managed changed; the late key alone is no reason to write")
	}

	// Without the fresh read the late key is invisible and would be replaced away.
	got, _ = ForUpdate(ctx, Defaults{Tags: sm("env", "prod")}, tm("app", "b"), prior, &d)
	if _, kept := got["late"]; kept {
		t.Fatal("fixture: state alone cannot know the late key")
	}
	got, _ = ForUpdate(ctx, Defaults{Tags: sm("env", "prod")}, tm("app", "b"), prior.WithCurrent(sm("app", "a", "env", "prod", "late", "x")), &d)
	assertMap(t, got, sm("app", "b", "env", "prod", "late", "x"))
}

// TestFinishReadPrunesStaleDefaultKeys: a recorded default key that is no
// longer a default and no longer on the resource is forgotten, so a key of that
// name added later outside Terraform is not taken for the provider's.
func TestFinishReadPrunesStaleDefaultKeys(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	p := fakePrivate{}
	RecordDefaults(ctx, p, Defaults{Tags: sm("env", "prod", "cost", "42", "gone", "x")}, &d)

	// env: still a default. cost: dropped from default_tags but still on the
	// resource, so the next write must clear it — kept. gone: neither — pruned.
	tags := types.MapNull(types.StringType)
	FinishRead(ctx, p, p, Defaults{Tags: sm("env", "prod")}, &tags, tm("env", "prod", "cost", "42"), &d)
	mustNoErr(t, d)
	keys := PriorDefaultKeys(ctx, p, &d)
	if len(keys) != 2 || keys[0] != "cost" || keys[1] != "env" {
		t.Fatalf("recorded default keys = %v, want [cost env]", keys)
	}

	// Unknown defaults prune nothing: which keys still apply is not known.
	FinishRead(ctx, p, p, Defaults{Unknown: true}, &tags, tm(), &d)
	if keys := PriorDefaultKeys(ctx, p, &d); len(keys) != 2 {
		t.Fatalf("unknown defaults pruned %v", keys)
	}
}
