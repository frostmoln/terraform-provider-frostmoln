package tftagstest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// NewResourceFunc builds the resource under test around a configured client.
type NewResourceFunc func(c *client.Client) resource.Resource

// RunDirect drives a taggable resource's own Create, Read and Update — no
// framework in between, so no private state — against the fake backend, and
// asserts the write contract request by request:
//
//   - a create with only provider default_tags sends them, and a key the
//     platform stamps (a tenant default) lands in tags_all, never in tags;
//   - a resource key overrides a default of the same key;
//   - a key added outside Terraform shows up in tags_all on refresh and leaves
//     tags alone;
//   - a default_tags change sends the new effective map and keeps every key the
//     provider does not manage;
//   - removing every tag actually clears them, in the backend's own spelling.
//
// The removal of a default the provider applied earlier needs private state,
// so it is asserted by the provider-wide gate, which runs through the real
// protocol server.
func RunDirect(t *testing.T, typeName string, newResource NewResourceFunc) {
	t.Helper()
	prof, ok := Profiles[typeName]
	if !ok {
		t.Fatalf("no tftagstest profile for %s", typeName)
	}
	ctx := context.Background()
	s := resourceSchema(t, newResource(nil))

	fake := NewFake(prof)
	fake.Stamp = map[string]string{"tenant-default": "td"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	withDefaults := func(d map[string]string) resource.Resource {
		c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client()), // pragma: allowlist secret
			client.WithDefaultTags(tftags.Defaults{Tags: d}))
		c.SetTenantIDForTest("t-1")
		return newResource(c)
	}

	t.Run("create with default_tags only", func(t *testing.T) {
		st := create(ctx, t, withDefaults(map[string]string{"env": "prod", "team": "ops"}), s, CreateConfig(t, s, prof, nil))
		assertSent(t, fake, http.MethodPost, prof.Field(prof.CreateField), map[string]string{"env": "prod", "team": "ops"})
		assertState(t, st, nil, map[string]string{"env": "prod", "team": "ops", "tenant-default": "td"}, prof)
	})

	var created tfsdk.State
	t.Run("a resource key overrides a default", func(t *testing.T) {
		created = create(ctx, t, withDefaults(map[string]string{"env": "prod", "team": "ops"}), s,
			CreateConfig(t, s, prof, map[string]string{"team": "web"}))
		assertSent(t, fake, http.MethodPost, prof.Field(prof.CreateField), map[string]string{"env": "prod", "team": "web"})
		assertState(t, created, map[string]string{"team": "web"}, map[string]string{"env": "prod", "team": "web", "tenant-default": "td"}, prof)
	})
	if created.Raw.IsNull() {
		t.FailNow()
	}

	var refreshed tfsdk.State
	t.Run("a key added outside Terraform lands in tags_all only", func(t *testing.T) {
		fake.Set("portal", "p")
		refreshed = read(ctx, t, withDefaults(map[string]string{"env": "prod", "team": "ops"}), created)
		assertState(t, refreshed, map[string]string{"team": "web"},
			map[string]string{"env": "prod", "team": "web", "tenant-default": "td", "portal": "p"}, prof)
	})

	t.Run("a default_tags change keeps unmanaged keys", func(t *testing.T) {
		st := update(ctx, t, withDefaults(map[string]string{"env": "staging"}), s, refreshed, map[string]string{"team": "web"})
		want := map[string]string{"env": "staging", "team": "web", "tenant-default": "td", "portal": "p"}
		assertSent(t, fake, prof.Update, prof.Field(prof.UpdateField), want)
		assertState(t, st, map[string]string{"team": "web"}, want, prof)
	})

	t.Run("removing every tag clears them", func(t *testing.T) {
		fake.Stamp = nil
		st := create(ctx, t, withDefaults(nil), s, CreateConfig(t, s, prof, map[string]string{"app": "a"}))
		st = update(ctx, t, withDefaults(nil), s, st, nil)
		w := fake.LastWrite(prof.Update)
		if w == nil {
			t.Fatal("no update reached the backend")
		}
		if prof.ClearFlag != "" {
			if string(w.Body[prof.ClearFlag]) != "true" {
				t.Errorf("the update did not set %s: %v", prof.ClearFlag, w.Body)
			}
		} else if raw := w.Body[prof.Field(prof.UpdateField)]; strings.TrimSpace(string(raw)) != "{}" {
			t.Errorf("the update sent %s = %s, want {} (an omitted or null field means KEEP)", prof.Field(prof.UpdateField), raw)
		}
		if len(fake.UserTags()) != 0 {
			t.Errorf("the backend still holds %v", fake.UserTags())
		}
		assertState(t, st, nil, map[string]string{}, prof)
	})
}

func resourceSchema(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics)
	}
	return sr.Schema
}

func create(ctx context.Context, t *testing.T, r resource.Resource, s schema.Schema, cfg tftypes.Value) tfsdk.State {
	t.Helper()
	planned := withAttrs(t, cfg, map[string]tftypes.Value{"tags_all": unknownMap()})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(ObjectType(t, s), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planned}, Config: tfsdk.Config{Schema: s, Raw: cfg}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	return resp.State
}

func read(ctx context.Context, t *testing.T, r resource.Resource, st tfsdk.State) tfsdk.State {
	t.Helper()
	resp := resource.ReadResponse{State: tfsdk.State{Schema: st.Schema, Raw: st.Raw.Copy()}}
	r.Read(ctx, resource.ReadRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read: the refresh removed the resource — the fake answered 404 for the path its Read asked for")
	}
	return resp.State
}

// update plans the given tags over prior (tags_all unknown, as a tag change
// plans it) and runs Update. The configuration is the planned state with every
// Computed-only attribute nulled, which is what it would be.
func update(ctx context.Context, t *testing.T, r resource.Resource, s schema.Schema, prior tfsdk.State, tags map[string]string) tfsdk.State {
	t.Helper()
	planned := withAttrs(t, prior.Raw, map[string]tftypes.Value{"tags": Tags(tags), "tags_all": unknownMap()})
	cfgAttrs := map[string]tftypes.Value{}
	for name, a := range s.Attributes {
		if a.IsComputed() && !a.IsOptional() && !a.IsRequired() {
			cfgAttrs[name] = tftypes.NewValue(ObjectType(t, s).AttributeTypes[name], nil)
		}
	}
	cfg := withAttrs(t, planned, cfgAttrs)
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: prior.Schema, Raw: planned}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:   tfsdk.Plan{Schema: prior.Schema, Raw: planned},
		State:  prior,
		Config: tfsdk.Config{Schema: prior.Schema, Raw: cfg},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	return resp.State
}

// withAttrs returns a copy of obj with the given attributes replaced. The copy
// matters: As hands back the value's own map, so writing into it would change
// obj too.
func withAttrs(t *testing.T, obj tftypes.Value, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	var attrs map[string]tftypes.Value
	if err := obj.As(&attrs); err != nil {
		t.Fatalf("not an object: %v", err)
	}
	out := make(map[string]tftypes.Value, len(attrs))
	for k, v := range attrs {
		out[k] = v
	}
	for k, v := range set {
		out[k] = v
	}
	return tftypes.NewValue(obj.Type(), out)
}

func unknownMap() tftypes.Value {
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, tftypes.UnknownValue)
}

func assertSent(t *testing.T, fake *Fake, method, field string, want map[string]string) {
	t.Helper()
	w := fake.LastWrite(method)
	if w == nil {
		t.Fatalf("no %s reached the backend", method)
	}
	got, ok := w.SentTags(t, field)
	if !ok {
		t.Fatalf("the %s carried no %q: %v", method, field, w.Body)
	}
	if !Equal(got, want) {
		t.Errorf("the %s sent %s = %v, want %v", method, field, got, want)
	}
}

// assertState checks tags (nil: null) and tags_all, and that no platform-owned
// key reached tags_all.
func assertState(t *testing.T, st tfsdk.State, wantTags, wantAll map[string]string, prof Profile) {
	t.Helper()
	tags := MapOf(t, Attr(t, st.Raw, "tags"))
	if wantTags == nil {
		if tags != nil {
			t.Errorf("tags = %v, want null", tags)
		}
	} else if !Equal(tags, wantTags) {
		t.Errorf("tags = %v, want exactly what the configuration names: %v", tags, wantTags)
	}
	allV := Attr(t, st.Raw, "tags_all")
	if !allV.IsKnown() || allV.IsNull() {
		t.Fatalf("tags_all = %v, want a known map", allV)
	}
	all := MapOf(t, allV)
	for k := range prof.Reserved {
		if _, leaked := all[k]; leaked {
			t.Errorf("platform-owned key %q reached tags_all", k)
		}
	}
	if !Equal(all, wantAll) {
		t.Errorf("tags_all = %v, want %v", all, wantAll)
	}
}
