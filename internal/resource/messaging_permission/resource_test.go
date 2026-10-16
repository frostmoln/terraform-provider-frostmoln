package messaging_permission

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj/messagingobjtest"
)

func setup(t *testing.T, fake *messagingobjtest.Fake) (*permissionResource, func()) {
	t.Helper()
	srv := messagingobjtest.NewServer(fake)
	return &permissionResource{client: messagingobjtest.Client(t, srv.URL), pollInterval: time.Millisecond}, srv.Close
}

func permSchema(t *testing.T) (resource.SchemaResponse, tftypes.Type) {
	t.Helper()
	var sr resource.SchemaResponse
	(&permissionResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr, sr.Schema.Type().TerraformType(context.Background())
}

func vals(conf, write, read string) map[string]string {
	return map[string]string{
		"instance_id": "inst-1", "user": "app", "vhost": "orders",
		"configure": conf, "write": write, "read": read,
	}
}

func readyDeps(fake *messagingobjtest.Fake) {
	fake.Seed("user", "inst-1", "app", "ready")
	fake.Seed("vhost", "inst-1", "orders", "ready")
}

func model(t *testing.T, st tfsdk.State) permissionModel {
	t.Helper()
	var m permissionModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state get: %v", d)
	}
	return m
}

func doCreate(t *testing.T, r *permissionResource) *resource.CreateResponse {
	t.Helper()
	sr, typ := permSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{
		Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, vals("^app\\.", ".*", ".*")),
	}}, resp)
	return resp
}

// update runs Update from the seeded grant (".*" x3) to the planned regexes.
func doUpdate(t *testing.T, r *permissionResource, conf string) *resource.UpdateResponse {
	t.Helper()
	sr, typ := permSchema(t)
	prior := vals(".*", ".*", ".*")
	prior["id"], prior["status"] = "inst-1/app/orders", "ready"
	state := tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, prior)}
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, vals(conf, ".*", ".*"))}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: plan.Raw}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)
	return resp
}

func TestOnlyTheRegexesUpdateInPlace(t *testing.T) {
	sr, _ := permSchema(t)
	for name, wantReplace := range map[string]bool{
		"instance_id": true, "user": true, "vhost": true, "configure": false, "write": false, "read": false,
	} {
		a := sr.Schema.Attributes[name].(schema.StringAttribute)
		if got := len(a.PlanModifiers) > 0; got != wantReplace {
			t.Errorf("%s: replaces=%v, want %v", name, got, wantReplace)
		}
	}
}

func TestCreateWaitsUntilReady(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 3}
	readyDeps(fake)
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	m := model(t, resp.State)
	if m.Status.ValueString() != "ready" || m.Configure.ValueString() != `^app\.` || m.ID.ValueString() != "inst-1/app/orders" {
		t.Errorf("state: status %q configure %q id %q", m.Status.ValueString(), m.Configure.ValueString(), m.ID.ValueString())
	}
}

// The PUT is an upsert, so a create must not silently take over a grant made
// outside this configuration.
func TestCreateRefusesAnExistingGrant(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	readyDeps(fake)
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "terraform import") {
		t.Fatalf("want an 'already exists, import it' error, got %v", resp.Diagnostics)
	}
	for _, req := range fake.Requests {
		if strings.HasPrefix(req, "PUT ") {
			t.Fatal("the existing grant was overwritten")
		}
	}
}

func TestCreateNotReadyExplainsOrdering(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("vhost", "inst-1", "orders", "ready") // the user does not exist yet
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "Reference the") {
		t.Fatalf("want the OBJECT_NOT_READY explanation, got %v", resp.Diagnostics)
	}
}

func TestUpdatePutsAndWaits(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 2}
	readyDeps(fake)
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doUpdate(t, r, "^$")
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if got := model(t, resp.State).Configure.ValueString(); got != "^$" {
		t.Errorf("configure = %q, want ^$", got)
	}
}

// THE plan-must-not-falsely-converge test: a grant replace that ends `ready`
// WITH a failureReason left the broker on the OLD grant. The apply fails and
// state holds the old regexes, so the next plan shows the change again.
func TestUpdateThatEndsReadyWithFailureReasonIsAnErrorAndKeepsTheBrokersGrant(t *testing.T) {
	fake := &messagingobjtest.Fake{Outcomes: map[string]messagingobjtest.Outcome{
		"replace permission app/orders": {Status: "ready", Reason: "BROKER_REFUSED", Message: "The broker refused the change."},
	}}
	readyDeps(fake)
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doUpdate(t, r, "^$")
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed replace must fail the apply")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"The broker refused the change.", "BROKER_REFUSED", "broker is unchanged"} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnostic lacks %q: %s", want, detail)
		}
	}
	m := model(t, resp.State)
	if m.Configure.ValueString() != ".*" {
		t.Errorf("state configure = %q: it must be the broker's grant (.*), never the plan's (^$)", m.Configure.ValueString())
	}
	if m.FailureReason.ValueString() != "BROKER_REFUSED" {
		t.Errorf("failure_reason = %q", m.FailureReason.ValueString())
	}
}

// A refused PUT never leaves the plan in state either.
func TestUpdateRefusedKeepsTheBrokersGrant(t *testing.T) {
	fake := &messagingobjtest.Fake{Refuse: map[string]messagingobjtest.Refusal{
		"PUT /users/app/permissions/orders": {Status: 409, Code: "ROTATION_IN_PROGRESS", Message: "rotation"},
	}}
	readyDeps(fake)
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doUpdate(t, r, "^$")
	if !resp.Diagnostics.HasError() {
		t.Fatal("want the refusal")
	}
	if got := model(t, resp.State).Configure.ValueString(); got != ".*" {
		t.Errorf("state configure = %q after a refused PUT, want the broker's .*", got)
	}
}

func TestDeleteWaitsUntilGone(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 2}
	readyDeps(fake)
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	sr, typ := permSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, vals(".*", ".*", ".*"))}
	resp := &resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || fake.Has("permission", "inst-1", "app/orders") {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
}

func TestReadNotFoundRemovesFromState(t *testing.T) {
	r, done := setup(t, &messagingobjtest.Fake{})
	defer done()

	sr, typ := permSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, vals(".*", ".*", ".*"))}
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatalf("want removal, got null=%v %v", resp.State.Raw.IsNull(), resp.Diagnostics)
	}
}

func TestImportState(t *testing.T) {
	sr, typ := permSchema(t)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&permissionResource{}).ImportState(context.Background(), resource.ImportStateRequest{ID: "inst-1/app/orders"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import: %v", resp.Diagnostics)
	}
	m := model(t, resp.State)
	if m.InstanceID.ValueString() != "inst-1" || m.User.ValueString() != "app" || m.Vhost.ValueString() != "orders" {
		t.Errorf("import set %q/%q/%q", m.InstanceID.ValueString(), m.User.ValueString(), m.Vhost.ValueString())
	}
	for _, bad := range []string{"inst-1/app", "inst-1/app/orders/x", "inst-1//orders"} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		(&permissionResource{}).ImportState(context.Background(), resource.ImportStateRequest{ID: bad}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import %q accepted", bad)
		}
	}
}

func TestModifyPlanReplacesAnErroredGrant(t *testing.T) {
	sr, typ := permSchema(t)
	v := vals(".*", ".*", ".*")
	v["status"] = "error"
	raw := messagingobjtest.Value(t, typ, v)
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: raw}}
	(&permissionResource{}).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		State: tfsdk.State{Schema: sr.Schema, Raw: raw}, Plan: tfsdk.Plan{Schema: sr.Schema, Raw: raw},
	}, resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 1 {
		t.Fatalf("want a replacement, got %v %v", resp.RequiresReplace, resp.Diagnostics)
	}
}
