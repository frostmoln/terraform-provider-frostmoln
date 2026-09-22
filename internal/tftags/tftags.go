// Package tftags is the one place a taggable resource's `tags`, `tags_all` and
// the provider's `default_tags` meet: what a write sends, what a read keeps,
// what a plan predicts, and what an import claims.
//
// # The model (AWS provider precedent, `default_tags` + `tags_all`)
//
//   - `tags` is exactly what the resource's configuration says. It is Optional
//     and never Computed, so it must read back as configured.
//   - `tags_all` is Computed: every tag the platform holds on the object after
//     reserved-key filtering — the provider's defaults, the resource's own tags,
//     and anything set outside Terraform (in the portal, by the fm CLI, or
//     stamped by the platform).
//   - A WRITE sends default_tags ∪ tags (a resource key wins) PLUS every key of
//     the platform's current set — read by the update itself, right before it
//     writes — that this provider does not manage. "Managed" means
//     the key was in the prior `tags` or among the default_tags keys the
//     provider applied at its last write (kept in private state). Every
//     Frostmoln tag update is a map REPLACE, so without the second half an
//     apply would wipe keys nobody asked Terraform to touch.
//   - A READ puts the API's value for each key `tags` names into `tags`; every
//     other key lives only in `tags_all`.
//
// # Why "clear" still needs care
//
// "The practitioner removed `tags` from the config" and "the practitioner has
// no opinion about tags" are different requests. Every Frostmoln update
// endpoint acts on tags only when the field is present, so an omitted field
// means KEEP — which is why an update always carries the full desired map, an
// empty one included, and why a caller's request struct must not `omitempty`
// that map.
package tftags

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Defaults is the provider's `default_tags` as a resource sees them.
type Defaults struct {
	// Tags holds every default whose value is known.
	Tags map[string]string
	// Unknown is set while any part of default_tags is not known yet — a plan
	// whose provider block references a value computed during the apply.
	// Terraform configures the provider again with known values before it
	// applies, so a write never sees Unknown; a plan predicts `tags_all` as
	// "(known after apply)" for every taggable resource instead of guessing.
	Unknown bool
}

// Keys returns the default keys in sorted order.
func (d Defaults) Keys() []string {
	keys := make([]string, 0, len(d.Tags))
	for k := range d.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Prior is a resource's tag state before a write.
type Prior struct {
	// Tags is the prior `tags`.
	Tags types.Map
	// TagsAll is the prior `tags_all`. It is null in state written before the
	// attribute existed; `tags` then held the whole filtered set, so it stands
	// in.
	TagsAll types.Map
	// DefaultKeys are the default_tags keys this provider applied at the last
	// write (see RecordDefaults).
	DefaultKeys []string
	// ImportPending is set from an import until the first write: the prior
	// `tags` came from the platform, not from a configuration, so its keys are
	// not taken as managed — a key the configuration does not name is kept.
	ImportPending bool

	// current is the platform's tag set as read right before a write (see
	// WithCurrent); it replaces TagsAll as the source of the unmanaged keys.
	current     map[string]string
	haveCurrent bool
}

// PriorOf assembles the prior tag state of a resource from its state and its
// private state.
func PriorOf(ctx context.Context, tags, tagsAll types.Map, private PrivateReader, diags *diag.Diagnostics) Prior {
	return Prior{
		Tags:          tags,
		TagsAll:       tagsAll,
		DefaultKeys:   PriorDefaultKeys(ctx, private, diags),
		ImportPending: importPending(ctx, private, diags),
	}
}

// WithCurrent returns p with the platform's current tag set, read by the write
// itself right before it builds its request. State is only as fresh as the
// last refresh: a key added between the plan and the apply (a `plan -out`
// applied later, a portal edit meanwhile) is not in it, and a write that
// derived the keys to keep from state alone would replace the map without it.
func (p Prior) WithCurrent(current map[string]string) Prior {
	p.current = current
	if p.current == nil {
		p.current = map[string]string{}
	}
	p.haveCurrent = true
	return p
}

// All returns the tag set the platform holds: the fresh read when there is
// one, else the last refresh's.
func (p Prior) All(ctx context.Context, diags *diag.Diagnostics) map[string]string {
	if p.haveCurrent {
		out := make(map[string]string, len(p.current))
		for k, v := range p.current {
			out[k] = v
		}
		return out
	}
	src := p.TagsAll
	if src.IsNull() || src.IsUnknown() {
		src = p.Tags
	}
	m, _ := knownMap(ctx, src, diags)
	return m
}

// unmanaged returns the keys of the platform's set this provider does not
// manage.
func (p Prior) unmanaged(ctx context.Context, diags *diag.Diagnostics) map[string]string {
	managed := make(map[string]bool, len(p.DefaultKeys))
	for _, k := range p.DefaultKeys {
		managed[k] = true
	}
	if !p.ImportPending {
		own, _ := knownMap(ctx, p.Tags, diags)
		for k := range own {
			managed[k] = true
		}
	}
	out := map[string]string{}
	for k, v := range p.All(ctx, diags) {
		if !managed[k] {
			out[k] = v
		}
	}
	return out
}

// Desired is the tag set the platform should hold after a write: the unmanaged
// keys of prior, overlaid by the provider defaults, overlaid by the resource's
// own tags. prior is nil on a create. ok is false when the result is not
// computable yet (the resource's tags or the defaults are unknown).
func Desired(ctx context.Context, d Defaults, tags types.Map, prior *Prior, diags *diag.Diagnostics) (map[string]string, bool) {
	if d.Unknown {
		return nil, false
	}
	own, ok := knownMap(ctx, tags, diags)
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	if prior != nil {
		for k, v := range prior.unmanaged(ctx, diags) {
			out[k] = v
		}
	}
	for k, v := range d.Tags {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v
	}
	return out, true
}

// ForCreate renders the tag map a create request carries: default_tags ∪ tags.
// nil when there is nothing to send, so a create that never had tags keeps its
// wire shape.
func ForCreate(ctx context.Context, d Defaults, tags types.Map, diags *diag.Diagnostics) map[string]string {
	if d.Unknown {
		diags.AddError("Provider default_tags Not Known",
			"The provider's default_tags are not known while applying. Terraform configures the provider "+
				"with known values before an apply, so this is a provider defect; please report it.")
		return nil
	}
	m, ok := Desired(ctx, d, tags, nil, diags)
	if !ok || len(m) == 0 {
		return nil
	}
	return m
}

// ForUpdate renders the tag map an update request carries and reports whether
// it differs from what the platform holds.
//
// The map is never nil when the desired set is known — an empty map is how
// tags are cleared, so the caller's field must not `omitempty` it. It is nil
// ("no opinion", which leaves the tags alone) only when the desired set is not
// computable. Callers that send tags only on a change use the bool; callers
// that always send them may ignore it.
func ForUpdate(ctx context.Context, d Defaults, tags types.Map, prior Prior, diags *diag.Diagnostics) (map[string]string, bool) {
	if d.Unknown {
		diags.AddError("Provider default_tags Not Known",
			"The provider's default_tags are not known while applying. Terraform configures the provider "+
				"with known values before an apply, so this is a provider defect; please report it.")
		return nil, false
	}
	m, ok := Desired(ctx, d, tags, &prior, diags)
	if !ok {
		return nil, false
	}
	return m, !Equal(m, prior.All(ctx, diags))
}

// ReadBack renders the tags an API read returned into `tags` and `tags_all`.
//
// apiTags must already have any platform-reserved keys removed (see
// internal/reservedmeta).
//
// tags_all is the API's set, verbatim, and always a known map.
//
// tags keeps exactly the keys prior names, each with the API's value, so a
// value changed or a key removed outside Terraform shows up as drift while a
// key added outside Terraform (a portal edit, a key the platform stamped) stays in
// tags_all. prior is the configuration's value on a create/update and the
// state's on a refresh. A null prior stays null; a known prior yields a known
// map, an empty one included, so `tags = {}` round-trips.
func ReadBack(ctx context.Context, apiTags map[string]string, prior types.Map, diags *diag.Diagnostics) (tags, tagsAll types.Map) {
	if apiTags == nil {
		apiTags = map[string]string{}
	}
	all, d := types.MapValueFrom(ctx, types.StringType, apiTags)
	diags.Append(d...)

	if prior.IsNull() || prior.IsUnknown() {
		return types.MapNull(types.StringType), all
	}
	kept := make(map[string]attr.Value, len(prior.Elements()))
	for k := range prior.Elements() {
		if v, ok := apiTags[k]; ok {
			kept[k] = types.StringValue(v)
		}
	}
	own, d := types.MapValue(types.StringType, kept)
	diags.Append(d...)
	return own, all
}

// PlanTagsAll is the plan-time half, called from every taggable resource's
// ModifyPlan. It marks `tags_all` "(known after apply)" on the first plan after
// an import, whenever the resource is updated for any reason, and whenever the
// next write would change the platform's tag set — the provider's
// default_tags changed, or a managed key drifted — and otherwise leaves the
// value the attribute's UseStateForUnknown kept. That is what makes a
// default_tags change plan an in-place update on EVERY taggable resource.
//
// A create always predicts unknown: the platform may add keys of its own.
//
// Whenever it plans that write it also plans `updated_at` unknown, on a
// resource that has one: the platform bumps it on the write.
//
// It never raises an error diagnostic of its own; a destroy's refresh plan runs
// through here too (see plan_error_diagnostics_test.go).
func PlanTagsAll(ctx context.Context, d Defaults, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	planTagsAll(ctx, d, req.Plan, req.State, req.Private, &resp.Plan, &resp.Diagnostics)
}

// planTagsAll is PlanTagsAll over its parts, so a test can hand it a private
// state (the framework's type cannot be constructed outside the framework).
func planTagsAll(ctx context.Context, d Defaults, plan tfsdk.Plan, state tfsdk.State, private PrivateReader, out *tfsdk.Plan, diags *diag.Diagnostics) {
	// The framework always hands ModifyPlan a response plan initialised from
	// the request; a test calling ModifyPlan directly may not, and there is
	// then nothing to write to.
	if plan.Raw.IsNull() || out.Schema == nil {
		return
	}
	tagsAllPath := path.Root("tags_all")
	// write plans an update that the framework may not have seen: when the
	// configuration equals state, it planned every other computed attribute at
	// its prior value, and the platform bumps updated_at on the write — a
	// stale planned timestamp fails the apply with an inconsistent result
	// (GitHub #2). A plan that writes nothing returns without calling it, so it
	// stays empty.
	write := func() {
		diags.Append(out.SetAttribute(ctx, tagsAllPath, types.MapUnknown(types.StringType))...)
		if _, ok := out.Schema.GetAttributes()["updated_at"]; ok {
			diags.Append(out.SetAttribute(ctx, path.Root("updated_at"), types.StringUnknown())...)
		}
	}
	if state.Raw.IsNull() {
		write()
		return
	}

	// The first plan after an import is never empty. Until a write, the
	// imported tags are not managed (Prior.ImportPending), and only a write ends
	// that — so an import whose configuration already matched would otherwise
	// never write, stay pending through every later apply, and turn the first
	// removal from `tags` into a no-op the plan had promised would happen. This
	// update writes nothing away: pending, every key the configuration does
	// not name is kept.
	if importPending(ctx, private, diags) {
		write()
		return
	}

	// Any update re-reads the resource, so tags_all can come back different
	// from state however little the update is about tags: a key added outside
	// Terraform since the last refresh is kept by the write and read back.
	// Pinning tags_all to state there would fail the apply with an inconsistent
	// result.
	if !plan.Raw.Equal(state.Raw) {
		write()
		return
	}

	var planTags, stateTags, stateTagsAll types.Map
	diags.Append(plan.GetAttribute(ctx, path.Root("tags"), &planTags)...)
	diags.Append(state.GetAttribute(ctx, path.Root("tags"), &stateTags)...)
	diags.Append(state.GetAttribute(ctx, tagsAllPath, &stateTagsAll)...)
	if diags.HasError() {
		return
	}

	prior := PriorOf(ctx, stateTags, stateTagsAll, private, diags)
	desired, ok := Desired(ctx, d, planTags, &prior, diags)
	if ok && Equal(desired, prior.All(ctx, diags)) {
		return
	}
	write()
}

// CurrentTagsReadFailed prefixes the error an update reports when it cannot
// read the tags the platform holds right before its write (Prior.WithCurrent).
const CurrentTagsReadFailed = "The update reads the tags the resource holds right before it writes them, so it " +
	"keeps the ones this configuration does not manage, and that read failed: "

// TagsAllDescription is the schema description every `tags_all` carries.
const TagsAllDescription = "Every tag the platform holds on this resource: the provider's `default_tags`, " +
	"this resource's own `tags` (which win on a shared key), and any key set outside Terraform — in the " +
	"portal, by the fm CLI, or stamped by the platform. Keys set outside Terraform appear only here and are " +
	"kept on every apply, never removed."

// TagsNote is appended to every taggable resource's `tags` description.
const TagsNote = " Merged with the provider's `default_tags` on every write (a key set here wins). " +
	"Holds only the keys this configuration sets; the full set is in `tags_all`."

// TagsAllAttribute is the `tags_all` schema attribute of every taggable
// resource.
func TagsAllAttribute() schema.MapAttribute {
	return schema.MapAttribute{
		Description: TagsAllDescription,
		Computed:    true,
		ElementType: types.StringType,
		PlanModifiers: []planmodifier.Map{
			mapplanmodifier.UseStateForUnknown(),
		},
	}
}

// Equal reports whether two tag maps hold the same pairs.
func Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// knownMap returns m's elements; ok is false when m or any element is
// unknown. A null map is an empty set.
func knownMap(ctx context.Context, m types.Map, diags *diag.Diagnostics) (map[string]string, bool) {
	out := map[string]string{}
	if m.IsUnknown() {
		return nil, false
	}
	if m.IsNull() {
		return out, true
	}
	for _, v := range m.Elements() {
		if v.IsUnknown() {
			return nil, false
		}
	}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out, true
}

// PrivateReader is the read half of a resource's private state
// (req.Private on Update, Read and ModifyPlan).
type PrivateReader interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

// PrivateWriter is the write half of a resource's private state
// (resp.Private on Create, Update, Read and ImportState).
type PrivateWriter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

const (
	// privateDefaultKeys holds the default_tags keys applied at the last write,
	// which is what lets a later write tell "a default the provider stopped
	// applying" (clear it) from "a key someone else put there" (keep it).
	privateDefaultKeys = "tftags_default_keys"
	// privateImported marks a state produced by ImportState, so the Read that
	// follows it fills `tags` from the platform.
	privateImported = "tftags_imported"
	// privateImportPending is set by that Read and cleared by the first write:
	// until then the imported `tags` are not taken as managed (Prior.ImportPending).
	privateImportPending = "tftags_import_pending"
)

func importPending(ctx context.Context, private PrivateReader, diags *diag.Diagnostics) bool {
	if isNil(private) {
		return false
	}
	raw, d := private.GetKey(ctx, privateImportPending)
	diags.Append(d...)
	return len(raw) > 0
}

// PriorDefaultKeys returns the default_tags keys recorded at the last write.
func PriorDefaultKeys(ctx context.Context, private PrivateReader, diags *diag.Diagnostics) []string {
	if isNil(private) {
		return nil
	}
	raw, d := private.GetKey(ctx, privateDefaultKeys)
	diags.Append(d...)
	if len(raw) == 0 {
		return nil
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		// Unreadable private state is treated as "no defaults recorded": the
		// write then keeps the keys instead of clearing them — the safe
		// direction.
		return nil
	}
	return keys
}

// RecordDefaults stores the default_tags keys a write just applied, and ends
// an import's pending state (the configuration's tags are now the managed
// ones). Call it once the write has reached the platform.
func RecordDefaults(ctx context.Context, private PrivateWriter, d Defaults, diags *diag.Diagnostics) {
	if isNil(private) {
		return
	}
	setDefaultKeys(ctx, private, d.Keys(), diags)
	diags.Append(private.SetKey(ctx, privateImportPending, nil)...)
}

func setDefaultKeys(ctx context.Context, private PrivateWriter, keys []string, diags *diag.Diagnostics) {
	var raw []byte
	if len(keys) > 0 {
		var err error
		if raw, err = json.Marshal(keys); err != nil {
			return
		}
	}
	diags.Append(private.SetKey(ctx, privateDefaultKeys, raw)...)
}

// MarkImported flags the state an ImportState produced; call it from every
// taggable resource's ImportState.
func MarkImported(ctx context.Context, private PrivateWriter, diags *diag.Diagnostics) {
	if isNil(private) {
		return
	}
	diags.Append(private.SetKey(ctx, privateImported, []byte("true"))...)
}

// FinishRead is the private-state half of every taggable resource's Read,
// called after the read-back has set tags and tags_all.
//
// After an import, nothing configured the resource's `tags` yet, so they are
// taken from the platform: tags_all minus every pair a provider default
// supplies with the same value (the AWS provider's rule — a key whose value
// differs from the default is an override and belongs in `tags`). A
// configuration that lists them all changes nothing on the platform. Until the
// first write those imported keys are not taken as managed
// (Prior.ImportPending): a key the configuration does not name — stamped by
// the platform, added in the portal — is kept, exactly as it is for a resource
// Terraform created. The plan shows it leaving `tags`, and it stays in
// tags_all. PlanTagsAll makes the first plan after an import an update, so
// that first write always happens and the pending state ends with it.
//
// On every Read it also forgets a recorded default key that no longer applies:
// one the provider's default_tags dropped while the key was gone from the
// resource anyway, so no write ever cleared it. Remembered, it would make a
// key of that name added later in the portal look managed, and the next write
// would remove it.
func FinishRead(ctx context.Context, req PrivateReader, resp PrivateWriter, d Defaults, tags *types.Map, tagsAll types.Map, diags *diag.Diagnostics) {
	if isNil(req) {
		return
	}
	all, _ := knownMap(ctx, tagsAll, diags)

	raw, dg := req.GetKey(ctx, privateImported)
	diags.Append(dg...)
	if len(raw) > 0 {
		own := map[string]attr.Value{}
		for k, v := range all {
			if dv, ok := d.Tags[k]; ok && dv == v {
				continue
			}
			own[k] = types.StringValue(v)
		}
		if len(own) == 0 {
			*tags = types.MapNull(types.StringType)
		} else {
			m, dg := types.MapValue(types.StringType, own)
			diags.Append(dg...)
			*tags = m
		}
		if isNil(resp) {
			return
		}
		setDefaultKeys(ctx, resp, d.Keys(), diags)
		diags.Append(resp.SetKey(ctx, privateImported, nil)...)
		diags.Append(resp.SetKey(ctx, privateImportPending, []byte("true"))...)
		return
	}

	if d.Unknown || isNil(resp) {
		return
	}
	recorded := PriorDefaultKeys(ctx, req, diags)
	kept := make([]string, 0, len(recorded))
	for _, k := range recorded {
		_, isDefault := d.Tags[k]
		_, onResource := all[k]
		if isDefault || onResource {
			kept = append(kept, k)
		}
	}
	if len(kept) != len(recorded) {
		setDefaultKeys(ctx, resp, kept, diags)
	}
}

// isNil reports whether v is nil or a typed nil pointer. The framework hands a
// resource a nil *privatestate.ProviderData only when a test calls its methods
// directly; SetKey on it is an error, so the helpers treat it as absent.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}
