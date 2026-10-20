package idform

import (
	"context"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Wrap returns r carrying the public-id switch (see the package comment). The
// wrapper implements every optional interface the provider's resources use and
// forwards to r when r does; where r does not, it behaves as the framework does
// without one (no import, no extra validation, no plan modification).
func Wrap(r resource.Resource) resource.Resource {
	return &wrapped{inner: r}
}

type wrapped struct {
	inner resource.Resource

	once         sync.Once
	computedOnly map[string]bool
	version      int64
	typeName     string
}

var (
	_ resource.ResourceWithConfigure        = (*wrapped)(nil)
	_ resource.ResourceWithImportState      = (*wrapped)(nil)
	_ resource.ResourceWithModifyPlan       = (*wrapped)(nil)
	_ resource.ResourceWithConfigValidators = (*wrapped)(nil)
	_ resource.ResourceWithValidateConfig   = (*wrapped)(nil)
	_ resource.ResourceWithUpgradeState     = (*wrapped)(nil)
)

// Inner returns the wrapped resource, for tests that inspect it.
func (w *wrapped) Inner() resource.Resource { return w.inner }

func (w *wrapped) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	w.inner.Metadata(ctx, req, resp)
	w.typeName = resp.TypeName
}

// load reads the inner schema once: its version and computed-only paths.
func (w *wrapped) load(ctx context.Context) {
	w.once.Do(func() {
		if w.typeName == "" {
			var m resource.MetadataResponse
			w.inner.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &m)
			w.typeName = m.TypeName
		}
		var resp resource.SchemaResponse
		w.inner.Schema(ctx, resource.SchemaRequest{}, &resp)
		w.version = resp.Schema.Version
		w.computedOnly = map[string]bool{}
		schemaWrap{w.isID, w.computedOnly}.schema(&resp.Schema)
	})
}

// isID reports whether a dotted attribute path holds ids: by name (id, *_id,
// *_ids) or by this type's kind map (security_groups, attached_to).
func (w *wrapped) isID(path string) bool {
	if idName(path) {
		return true
	}
	_, ok := Kinds[w.typeName][path]
	return ok
}

func (w *wrapped) rules(m mode) rules { return rules{m, w.isID} }

func (w *wrapped) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	w.load(ctx)
	w.inner.Schema(ctx, req, resp)
	resp.Schema.Version++
	schemaWrap{w.isID, map[string]bool{}}.schema(&resp.Schema)
}

func (w *wrapped) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	w.load(ctx)
	var inner map[int64]resource.StateUpgrader
	if u, ok := w.inner.(resource.ResourceWithUpgradeState); ok {
		inner = u.UpgradeState(ctx)
	}
	return upgraders(ctx, inner, w.version, Kinds[w.typeName])
}

func (w *wrapped) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c, ok := w.inner.(resource.ResourceWithConfigure); ok {
		c.Configure(ctx, req, resp)
	}
}

func (w *wrapped) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	i, ok := w.inner.(resource.ResourceWithImportState)
	if !ok {
		resp.Diagnostics.AddError("Resource Import Not Implemented",
			"This resource does not support import. Please contact the provider developer for additional information.")
		return
	}
	i.ImportState(ctx, req, resp)
}

func (w *wrapped) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if m, ok := w.inner.(resource.ResourceWithModifyPlan); ok {
		m.ModifyPlan(ctx, req, resp)
	}
}

func (w *wrapped) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	if v, ok := w.inner.(resource.ResourceWithConfigValidators); ok {
		return v.ConfigValidators(ctx)
	}
	return nil
}

func (w *wrapped) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if v, ok := w.inner.(resource.ResourceWithValidateConfig); ok {
		v.ValidateConfig(ctx, req, resp)
	}
}

// Create keeps every planned value the API echoed in the other spelling: the
// framework requires the applied state to match the plan.
func (w *wrapped) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	w.load(ctx)
	w.inner.Create(ctx, req, resp)
	if !resp.State.Raw.IsNull() {
		resp.State.Raw = keepPrior(resp.State.Raw, req.Plan.Raw, w.rules(loose), nil)
	}
}

// Read keeps a prior value the API now spells differently, unless the
// attribute is computed-only (no config can hold it, so the API's spelling is
// the one to keep) or both are public ids of different kinds (the API's kind
// wins).
func (w *wrapped) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	w.load(ctx)
	w.inner.Read(ctx, req, resp)
	if !resp.State.Raw.IsNull() {
		resp.State.Raw = keepPrior(resp.State.Raw, req.State.Raw, w.rules(strict), w.computedOnly)
	}
}

// Update sends nothing when plan and state differ only in id spelling (a UUID
// hardcoded in HCL against public state): the state becomes the plan. A plan
// with any unknown value is a real change (a computed value the apply derives,
// such as tags_all under a default_tags change) and always updates, because
// same never matches an unknown against known state. Otherwise it keeps
// planned spellings as Create does.
func (w *wrapped) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	w.load(ctx)
	if same(req.Plan.Raw, req.State.Raw, w.rules(strict), "") {
		resp.State.Raw = req.Plan.Raw
		return
	}
	w.inner.Update(ctx, req, resp)
	if !resp.State.Raw.IsNull() {
		resp.State.Raw = keepPrior(resp.State.Raw, req.Plan.Raw, w.rules(loose), nil)
	}
}

func (w *wrapped) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	w.inner.Delete(ctx, req, resp)
}
