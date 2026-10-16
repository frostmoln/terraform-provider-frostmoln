package messaging_user

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj/messagingobjtest"
)

func setup(t *testing.T, fake *messagingobjtest.Fake) (*userResource, func()) {
	t.Helper()
	srv := messagingobjtest.NewServer(fake)
	return &userResource{client: messagingobjtest.Client(t, srv.URL), pollInterval: time.Millisecond}, srv.Close
}

func userSchema(t *testing.T) (resource.SchemaResponse, tftypes.Type) {
	t.Helper()
	var sr resource.SchemaResponse
	(&userResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr, sr.Schema.Type().TerraformType(context.Background())
}

func model(t *testing.T, st tfsdk.State) userModel {
	t.Helper()
	var m userModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state get: %v", d)
	}
	return m
}

func TestPasswordIsSensitiveAndCarriesTheStateNote(t *testing.T) {
	sr, _ := userSchema(t)
	pw, ok := sr.Schema.Attributes["password"].(schema.StringAttribute)
	if !ok || !pw.Sensitive || !pw.Computed || pw.Optional || pw.Required {
		t.Fatalf("password must be Computed+Sensitive only, got %+v", pw)
	}
	if !strings.Contains(pw.Description, docs.StateSecretNote) {
		t.Error("password must carry docs.StateSecretNote")
	}
}

// The password never comes in the create response; it is read from
// .../credentials once the user is ready.
func TestCreateWaitsThenReadsThePassword(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 2}
	r, done := setup(t, fake)
	defer done()

	sr, typ := userSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	plan := messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": "app"})
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: plan}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	m := model(t, resp.State)
	if m.Status.ValueString() != "ready" || m.Password.ValueString() != "pw-app" { // pragma: allowlist secret
		t.Fatalf("status %q password set=%v, want ready with the credentials' password",
			m.Status.ValueString(), m.Password.ValueString() == "pw-app") // pragma: allowlist secret
	}
	if fake.CredReads != 1 {
		t.Errorf("credentials read %d times, want 1", fake.CredReads)
	}
}

func TestCreateThatLandsInErrorFailsWithoutAPassword(t *testing.T) {
	fake := &messagingobjtest.Fake{Outcomes: map[string]messagingobjtest.Outcome{
		"create user app": {Status: "error", Reason: "IN_FLIGHT", Message: "Too many operations were queued."},
	}}
	r, done := setup(t, fake)
	defer done()

	sr, typ := userSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	plan := messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": "app"})
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: plan}}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "Too many operations") {
		t.Fatalf("want the failure message in an error, got %v", resp.Diagnostics)
	}
	m := model(t, resp.State)
	if m.Status.ValueString() != "error" || !m.Password.IsNull() {
		t.Errorf("status %q password null=%v, want error and null", m.Status.ValueString(), m.Password.IsNull())
	}
	if fake.CredReads != 0 {
		t.Error("credentials must not be read for a user that is not ready")
	}
}

// An import sets only the ids; the Read that follows reads the password.
func TestImportThenReadFetchesThePassword(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("user", "inst-1", "app", "ready")
	r, done := setup(t, fake)
	defer done()

	sr, typ := userSchema(t)
	imp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "inst-1/app"}, imp)
	if imp.Diagnostics.HasError() {
		t.Fatalf("import: %v", imp.Diagnostics)
	}
	resp := &resource.ReadResponse{State: imp.State}
	r.Read(context.Background(), resource.ReadRequest{State: imp.State}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	m := model(t, resp.State)
	if m.Password.ValueString() != "pw-app" || m.ID.ValueString() != "inst-1/app" { // pragma: allowlist secret
		t.Errorf("after import: id %q password set=%v", m.ID.ValueString(), m.Password.ValueString() == "pw-app") // pragma: allowlist secret
	}
}

func TestImportRejectsMalformedIDs(t *testing.T) {
	sr, typ := userSchema(t)
	for _, id := range []string{"inst-1", "inst-1/a/b", "/app", "inst-1/."} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		(&userResource{}).ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import %q accepted", id)
		}
	}
}

func TestReadNotFoundRemovesFromState(t *testing.T) {
	r, done := setup(t, &messagingobjtest.Fake{})
	defer done()

	sr, typ := userSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, map[string]string{
		"id": "inst-1/app", "instance_id": "inst-1", "name": "app", "password": "old", // pragma: allowlist secret
	})}
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatalf("want removal without error, got null=%v %v", resp.State.Raw.IsNull(), resp.Diagnostics)
	}
}

func TestDeleteInUseNamesTheBlockingVhosts(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("user", "inst-1", "app", "ready")
	fake.Seed("permission", "inst-1", "app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	sr, typ := userSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, map[string]string{
		"id": "inst-1/app", "instance_id": "inst-1", "name": "app",
	})}
	resp := &resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), `"orders"`) {
		t.Fatalf("want OBJECT_IN_USE naming vhost orders, got %v", resp.Diagnostics)
	}
}

func TestDelete202WaitsUntilGone(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 2}
	fake.Seed("user", "inst-1", "app", "ready")
	r, done := setup(t, fake)
	defer done()

	sr, typ := userSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, map[string]string{
		"id": "inst-1/app", "instance_id": "inst-1", "name": "app",
	})}
	resp := &resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || fake.Has("user", "inst-1", "app") {
		t.Fatalf("delete: %v (still there: %v)", resp.Diagnostics, fake.Has("user", "inst-1", "app"))
	}
}

// The password must land in the same apply even when the credentials endpoint
// is briefly unavailable right after the user turns ready.
func TestCreateRetriesTheCredentialsRead(t *testing.T) {
	fake := &messagingobjtest.Fake{CredFailures: 2}
	r, done := setup(t, fake)
	defer done()

	sr, typ := userSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	plan := messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": "app"})
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: plan}}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 0 {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if got := model(t, resp.State).Password.ValueString(); got != "pw-app" { // pragma: allowlist secret
		t.Fatalf("password not read after retries (credentials reads: %d)", fake.CredReads)
	}
	if fake.CredReads != 3 {
		t.Errorf("credentials read %d times, want 3", fake.CredReads)
	}
}
