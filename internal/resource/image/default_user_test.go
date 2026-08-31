package image

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestFromAPIRefreshesDefaultUserFromTheRawProperty is the trap this attribute
// exists inside: the response carries TWO answers to "who logs in".
//
//   - `defaultUser` is DERIVED by compute — the explicit property, else
//     os_admin_user, else its os_distro map. An image whose user was never set
//     still reads back "debian" from the distro map.
//   - `metadata["default_user"]` is RAW — the customer-set property, and empty
//     on every image that never set one.
//
// default_user is Optional and NOT Computed, so refreshing it from the derived
// value would write into state a value the practitioner never configured: a
// permanent diff on every plan, or an outright "provider produced inconsistent
// result after apply" on the create that adopted it.
func TestFromAPIRefreshesDefaultUserFromTheRawProperty(t *testing.T) {
	// Never set: the derived answer is present and must be ignored.
	var m ImageModel
	m.fromAPI(&apiImage{
		ID: "img-1", OSDistro: "debian",
		Metadata: map[string]string{"os_distro": "debian"},
	})
	if !m.DefaultUser.IsNull() {
		t.Errorf("an image with no default_user property must refresh to null, got %v", m.DefaultUser)
	}

	// Explicitly set: the raw property is what lands in state.
	m = ImageModel{}
	m.fromAPI(&apiImage{
		ID: "img-2", OSDistro: "sortix",
		Metadata: map[string]string{"default_user": "sortixer"},
	})
	if m.DefaultUser.ValueString() != "sortixer" {
		t.Errorf("default_user = %v, want sortixer", m.DefaultUser)
	}

	// Cleared server-side while the config still names one: state must show the
	// clear, so the next plan offers to put it back rather than hiding the drift.
	m = ImageModel{DefaultUser: types.StringValue("sortixer")}
	m.fromAPI(&apiImage{ID: "img-3", Metadata: map[string]string{}})
	if !m.DefaultUser.IsNull() {
		t.Errorf("a cleared default_user must refresh to null, got %v", m.DefaultUser)
	}

	// The clear is closed the other way round by TestUpdateClearsDefaultUser
	// below, which drives Update and reads the wire. An earlier version asserted
	// `types.StringNull().ValueString() == ""` here instead — a tautology about
	// the framework that would have stayed green with the Update branch deleted.
}

// TestToCreateRequestCarriesDefaultUser: the create payload is the only place a
// practitioner can name the login user for an image whose distro the platform
// does not know, and without it a console-password launch of that image is
// REFUSED rather than degraded.
func TestToCreateRequestCarriesDefaultUser(t *testing.T) {
	m := &ImageModel{
		Name:            types.StringValue("my-nixos"),
		DiskFormat:      types.StringValue("qcow2"),
		ContainerFormat: types.StringValue("bare"),
		DefaultUser:     types.StringValue("nixos"),
	}
	if got := m.toCreateRequest().DefaultUser; got != "nixos" {
		t.Errorf("create payload defaultUser = %q, want nixos", got)
	}

	// Omitted stays empty, and the field is `omitempty`, so it is not sent.
	if got := (&ImageModel{Name: types.StringValue("x")}).toCreateRequest().DefaultUser; got != "" {
		t.Errorf("an unset default_user must send nothing, got %q", got)
	}
}

// TestDefaultUserIsNotCreateOnly pins the correction that shaped this attribute:
// compute updates the property IN PLACE (domain.UpdateImageRequest.DefaultUser),
// so default_user must NOT be RequiresReplace like os_distro. Forcing a whole
// image replacement — re-upload, re-import, a new id, and every dependent
// instance replaced — for a one-word metadata fix would be the opposite of the
// self-service this attribute is for.
func TestDefaultUserIsNotCreateOnly(t *testing.T) {
	var resp resource.SchemaResponse
	(&imageResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["default_user"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("default_user is missing or not a StringAttribute: %T", resp.Schema.Attributes["default_user"])
	}
	if !attr.Optional || attr.Required {
		t.Errorf("default_user must be Optional and not Required: %+v", attr)
	}
	// os_distro carries RequiresReplace and default_user must not — comparing
	// against the sibling proves the check is looking at the right thing.
	if len(attr.PlanModifiers) != 0 {
		t.Errorf("default_user must carry NO plan modifiers (it changes in place), got %d", len(attr.PlanModifiers))
	}
	osDistro, ok := resp.Schema.Attributes["os_distro"].(schema.StringAttribute)
	if !ok || len(osDistro.PlanModifiers) == 0 {
		t.Fatal("os_distro should still be the create-only sibling with a RequiresReplace modifier")
	}
}

// TestDefaultUserRejectsPaddingAtPlanTime: compute REFUSES padding rather than
// trimming it, precisely so a stored value can never differ from the value sent
// — a server that trimmed would hand back something the configuration did not
// say, which is "provider produced inconsistent result after apply" and then a
// loop, because Update could not persist the padding either. This validator is
// byte-identical to that rule, so the failure lands at plan time with a message
// naming the rule instead of as a 400 mid-apply.
func TestDefaultUserRejectsPaddingAtPlanTime(t *testing.T) {
	var resp resource.SchemaResponse
	(&imageResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["default_user"].(schema.StringAttribute)
	if !ok {
		t.Fatal("default_user is missing or not a StringAttribute")
	}
	if len(attr.Validators) == 0 {
		t.Fatal("default_user has no plan-time validator")
	}

	for _, tc := range []struct {
		value string
		valid bool
	}{
		// `default_user = ""` is REFUSED, unlike the wire and the OpenAPI
		// pattern. Clearing is spelled by removing the attribute, which plans as
		// null (never validated) and which Update sends as "". An explicit ""
		// would be dropped from the create body by omitempty, read back as null,
		// and hard-fail the apply against the planned "" — forever.
		{"", false},
		{"debian", true},
		{"cloud-user", true},
		{"Administrator", true}, // Windows, which compute's own read path derives
		{" debian ", false},     // padding: refused here and by the server
		{"my user", false},
		{"1user", false},
		{"a.b", false}, // provisioning refuses dots, so compute does too
		{strings.Repeat("a", 33), false},
	} {
		var vResp validator.StringResponse
		attr.Validators[0].ValidateString(context.Background(), validator.StringRequest{
			ConfigValue: types.StringValue(tc.value),
			Path:        path.Root("default_user"),
		}, &vResp)
		if got := !vResp.Diagnostics.HasError(); got != tc.valid {
			t.Errorf("default_user %q: valid = %v, want %v", tc.value, got, tc.valid)
		}
	}
}

// TestUpdateClearsDefaultUser drives Update and reads the wire.
//
// On the HCL surface "clear the login user" is spelled by REMOVING the
// attribute, which plans as null; on the wire it is spelled `""`. Nothing but a
// real Update proves the two are joined — and this is the half a plan-time
// validator cannot cover, since it never sees a null.
func TestUpdateClearsDefaultUser(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewDecoder(req.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(apiImage{
			ID: "img-1", Name: "custom-ubuntu", Status: "active", Visibility: "private", Owner: "proj-1",
		})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())
	s := resourceSchema(t)

	// State holds a login user; the plan drops the attribute.
	stateAttrsWithUser := stateAttrs("img-1")
	stateAttrsWithUser["default_user"] = tftypes.NewValue(tftypes.String, "sortixer")
	stateVal := objectValue(s, stateAttrsWithUser)
	planVal := objectValue(s, stateAttrs("img-1")) // default_user null

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", resp.Diagnostics.Errors())
	}
	v, present := body["defaultUser"]
	if !present {
		t.Fatalf("removing default_user sent no clear at all (body: %v)", body)
	}
	if v != "" {
		t.Errorf("clear sent %v, want the empty string compute reads as 'drop the property'", v)
	}
}
