package messaging_vhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/failclosed"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj/messagingobjtest"
)

func setup(t *testing.T, fake *messagingobjtest.Fake) (*vhostResource, func()) {
	t.Helper()
	srv := messagingobjtest.NewServer(fake)
	return &vhostResource{client: messagingobjtest.Client(t, srv.URL), pollInterval: time.Millisecond}, srv.Close
}

func vhostSchema(t *testing.T) (resource.SchemaResponse, tftypes.Type) {
	t.Helper()
	var sr resource.SchemaResponse
	(&vhostResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr, sr.Schema.Type().TerraformType(context.Background())
}

func doCreate(t *testing.T, r *vhostResource, name string) *resource.CreateResponse {
	t.Helper()
	sr, typ := vhostSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	plan := messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": name})
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: plan}}, resp)
	return resp
}

func stateOf(t *testing.T, name string) tfsdk.State {
	t.Helper()
	sr, typ := vhostSchema(t)
	return tfsdk.State{Schema: sr.Schema, Raw: messagingobjtest.Value(t, typ, map[string]string{
		"id": "inst-1/" + name, "instance_id": "inst-1", "name": name, "status": "ready",
	})}
}

func doDelete(t *testing.T, r *vhostResource, name string) *resource.DeleteResponse {
	t.Helper()
	st := stateOf(t, name)
	resp := &resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	return resp
}

func model(t *testing.T, st tfsdk.State) vhostModel {
	t.Helper()
	var m vhostModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state get: %v", d)
	}
	return m
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	NewResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_messaging_vhost" {
		t.Errorf("type name = %q", resp.TypeName)
	}
}

// Create returns only once the platform's verdict has landed, and records it.
func TestCreateWaitsUntilReady(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 3}
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r, "orders")
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	m := model(t, resp.State)
	if m.Status.ValueString() != messagingobj.StatusReady || m.ID.ValueString() != "inst-1/orders" {
		t.Fatalf("state = status %q id %q, want ready inst-1/orders", m.Status.ValueString(), m.ID.ValueString())
	}
	if !m.FailureReason.IsNull() {
		t.Errorf("failure_reason = %q, want null", m.FailureReason.ValueString())
	}
	gets := 0
	for _, req := range fake.Requests {
		if req == "GET /vhosts/orders" {
			gets++
		}
	}
	if gets < 3 {
		t.Errorf("polled %d times, want at least 3 (the verdict lands on the third)", gets)
	}
}

// A failed create is an error, and the object is recorded (so Terraform taints
// it and the next apply deletes and re-creates it), never silently converged.
func TestCreateThatLandsInErrorFails(t *testing.T) {
	fake := &messagingobjtest.Fake{Outcomes: map[string]messagingobjtest.Outcome{
		"create vhost orders": {Status: "error", Reason: "NOT_DELIVERED", Message: "The agent did not receive it."},
	}}
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r, "orders")
	if !resp.Diagnostics.HasError() {
		t.Fatal("a create that lands in error must fail the apply")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"The agent did not receive it.", "NOT_DELIVERED", "broker is unchanged", "tainted"} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnostic lacks %q: %s", want, detail)
		}
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("the failed object must be recorded so the next apply deletes it first")
	}
	if got := model(t, resp.State).Status.ValueString(); got != "error" {
		t.Errorf("status = %q, want error", got)
	}
}

func TestCreateCapabilityAbsentExplains(t *testing.T) {
	fake := &messagingobjtest.Fake{Refuse: map[string]messagingobjtest.Refusal{
		"POST /vhosts": {Status: http.StatusConflict, Code: "CAPABILITY_ABSENT", Message: "This instance cannot manage vhosts and users yet."},
	}}
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r, "orders")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "will not help") {
		t.Fatalf("want the CAPABILITY_ABSENT explanation, got %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("nothing was created; nothing may be recorded")
	}
}

// A 204 is a finished delete: the broker never held the vhost, so there is
// nothing to wait for.
func TestDelete204IsDone(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("vhost", "inst-1", "orders", "error") // a failed create: never reached the broker
	r, done := setup(t, fake)
	defer done()

	resp := doDelete(t, r, "orders")
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if fake.Has("vhost", "inst-1", "orders") {
		t.Error("the vhost is still there")
	}
	if last := fake.Requests[len(fake.Requests)-1]; last != "DELETE /vhosts/orders" {
		t.Errorf("a 204 must not be polled; last request %q", last)
	}
}

func TestDelete202WaitsUntilGone(t *testing.T) {
	fake := &messagingobjtest.Fake{PollsUntilDone: 2}
	fake.Seed("vhost", "inst-1", "orders", "ready")
	r, done := setup(t, fake)
	defer done()

	if resp := doDelete(t, r, "orders"); resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if fake.Has("vhost", "inst-1", "orders") {
		t.Error("Delete returned before the vhost was gone")
	}
}

// `ready` WITH a failureReason after a delete is a FAILED delete: the vhost
// and its messages are still there, and saying "destroyed" would drop it.
func TestDeleteThatEndsReadyWithFailureReasonIsAnError(t *testing.T) {
	fake := &messagingobjtest.Fake{Outcomes: map[string]messagingobjtest.Outcome{
		"delete vhost orders": {Status: "ready", Reason: "BROKER_REFUSED", Message: "The broker refused the change."},
	}}
	fake.Seed("vhost", "inst-1", "orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doDelete(t, r, "orders")
	if !resp.Diagnostics.HasError() {
		t.Fatal("a delete that ends ready+failureReason must fail")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"The broker refused the change.", "BROKER_REFUSED", "broker is unchanged"} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnostic lacks %q: %s", want, detail)
		}
	}
}

func TestDeleteInUseNamesTheBlockingGrants(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("vhost", "inst-1", "orders", "ready")
	fake.Seed("permission", "inst-1", "billing-app/orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doDelete(t, r, "orders")
	if !resp.Diagnostics.HasError() {
		t.Fatal("want OBJECT_IN_USE to fail the destroy")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(d, `"billing-app"`) {
		t.Errorf("diagnostic does not name the blocking user: %s", d)
	}
}

func TestReadNotFoundRemovesFromState(t *testing.T) {
	r, done := setup(t, &messagingobjtest.Fake{})
	defer done()

	st := stateOf(t, "orders")
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a vhost the service says is gone must leave state")
	}
}

func TestReadGatewayNotRoutedKeepsState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" {
			_, _ = w.Write([]byte(`{"id":"u","tenantId":"` + messagingobjtest.TenantID + `"}`))
			return
		}
		failclosed.GatewayNotRouted(w)
	}))
	defer srv.Close()
	r := &vhostResource{client: messagingobjtest.Client(t, srv.URL)}
	st := stateOf(t, "orders")
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	failclosed.AssertReadKeepsState(t, "messaging_vhost", resp.State.Raw.IsNull(), resp.Diagnostics.HasError())
}

func TestImportState(t *testing.T) {
	sr, typ := vhostSchema(t)
	for _, tc := range []struct {
		id      string
		wantErr bool
	}{
		{"inst-1/orders", false},
		{"inst-1", true},
		{"inst-1/a/b", true},
		{"inst-1/", true},
		{"inst-1/..", true},
	} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		(&vhostResource{}).ImportState(context.Background(), resource.ImportStateRequest{ID: tc.id}, resp)
		if got := resp.Diagnostics.HasError(); got != tc.wantErr {
			t.Errorf("import %q: error = %v, want %v (%v)", tc.id, got, tc.wantErr, resp.Diagnostics)
			continue
		}
		if !tc.wantErr {
			m := model(t, resp.State)
			if m.InstanceID.ValueString() != "inst-1" || m.Name.ValueString() != "orders" || m.ID.ValueString() != tc.id {
				t.Errorf("import %q set instance_id=%q name=%q id=%q", tc.id, m.InstanceID.ValueString(), m.Name.ValueString(), m.ID.ValueString())
			}
		}
	}
}

// A 403 while waiting (a key that lost its scope mid-apply) fails at once
// instead of holding the instance's lock for the whole budget.
func TestCreateFailsFastOnA403DuringTheWait(t *testing.T) {
	fake := &messagingobjtest.Fake{Refuse: map[string]messagingobjtest.Refusal{
		"GET /vhosts/orders": {Status: http.StatusForbidden, Code: "forbidden", Message: "missing scope messaging:read"},
	}}
	r, done := setup(t, fake)
	defer done()

	start := time.Now()
	resp := doCreate(t, r, "orders")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "missing scope") {
		t.Fatalf("want the 403 in the diagnostic, got %v", resp.Diagnostics)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a 403 was retried for %s", elapsed)
	}
}

func TestObjectCallsAskForEnglish(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	r, done := setup(t, fake)
	defer done()

	if resp := doCreate(t, r, "orders"); resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	for i, lang := range fake.Languages {
		if lang != "en" {
			t.Errorf("request %q sent Accept-Language %q, want en", fake.Requests[i], lang)
		}
	}
}

func modifyPlan(t *testing.T, status string) *resource.ModifyPlanResponse {
	t.Helper()
	sr, typ := vhostSchema(t)
	raw := messagingobjtest.Value(t, typ, map[string]string{
		"id": "inst-1/orders", "instance_id": "inst-1", "name": "orders", "status": status,
	})
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: raw}}
	(&vhostResource{}).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		State: tfsdk.State{Schema: sr.Schema, Raw: raw},
		Plan:  tfsdk.Plan{Schema: sr.Schema, Raw: raw},
	}, resp)
	return resp
}

// An `error` vhost that is not tainted (imported, or untainted) must not plan
// clean: the vhost is absent or unknown on the broker.
func TestModifyPlanReplacesAnErroredVhost(t *testing.T) {
	resp := modifyPlan(t, "error")
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 1 {
		t.Fatalf("want one requires-replace path, got %v %v", resp.RequiresReplace, resp.Diagnostics)
	}
	var m vhostModel
	if d := resp.Plan.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	if !m.Status.IsUnknown() {
		t.Error("status must be planned unknown, or Terraform drops the replacement as a no-op")
	}
	if ok := modifyPlan(t, "ready"); len(ok.RequiresReplace) != 0 {
		t.Errorf("a ready vhost must plan clean, got %v", ok.RequiresReplace)
	}
}

func TestReadWarnsOnAnErroredVhost(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("vhost", "inst-1", "orders", "error")
	r, done := setup(t, fake)
	defer done()

	st := stateOf(t, "orders")
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("want one warning, got %v", resp.Diagnostics)
	}
}

// The service matches names EXACTLY (only its uniqueness check folds case),
// so `orders` -> `Orders` is a different vhost: it must plan a replacement,
// never an in-place rename that would record a name the server does not hold.
func TestACaseOnlyNameChangeReplaces(t *testing.T) {
	sr, typ := vhostSchema(t)
	attr, ok := sr.Schema.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatal("name is not a string attribute")
	}
	prior := messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": "orders", "id": "inst-1/orders"})
	next := messagingobjtest.Value(t, typ, map[string]string{"instance_id": "inst-1", "name": "Orders"})
	req := planmodifier.StringRequest{
		Path:        path.Root("name"),
		StateValue:  types.StringValue("orders"),
		PlanValue:   types.StringValue("Orders"),
		ConfigValue: types.StringValue("Orders"),
		State:       tfsdk.State{Schema: sr.Schema, Raw: prior},
		Plan:        tfsdk.Plan{Schema: sr.Schema, Raw: next},
		Config:      tfsdk.Config{Schema: sr.Schema, Raw: next},
	}
	resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
	for _, m := range attr.PlanModifiers {
		m.PlanModifyString(context.Background(), req, resp)
	}
	if !resp.RequiresReplace {
		t.Fatal("orders -> Orders must replace the vhost")
	}
}

// The fake mirrors the service: a different-case duplicate is refused
// (OBJECT_EXISTS) while the original exists — which is why the replacement
// deletes first.
func TestCreatingADifferentCaseDuplicateIsRefused(t *testing.T) {
	fake := &messagingobjtest.Fake{}
	fake.Seed("vhost", "inst-1", "orders", "ready")
	r, done := setup(t, fake)
	defer done()

	resp := doCreate(t, r, "Orders")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "OBJECT_EXISTS") {
		t.Fatalf("want OBJECT_EXISTS, got %v", resp.Diagnostics)
	}
	if doDelete(t, r, "orders").Diagnostics.HasError() {
		t.Fatal("delete of the original failed")
	}
	if resp := doCreate(t, r, "Orders"); resp.Diagnostics.HasError() {
		t.Fatalf("after the original is gone the new spelling must create: %v", resp.Diagnostics)
	}
}
