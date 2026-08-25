package ssh_key

import (
	"context"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/failclosed"
)

// ssh_key is the COMPUTE-backed instance of the fail-closed contract. See
// internal/failclosed for the rule, and for why the ordinary not-found fixtures
// cannot pin it.

func sshKeyFailClosedState(t *testing.T) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(sshKeyTFType(t), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "test-key"),
		"name":        tftypes.NewValue(tftypes.String, "test-key"),
		"public_key":  tftypes.NewValue(tftypes.String, "ssh-ed25519 AAAA..."),
		"fingerprint": tftypes.NewValue(tftypes.String, "SHA256:xyz"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})
}

func TestSSHKeyRead_GatewayNotRouted_KeepsState(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := sshKeySchema(t)
	raw := sshKeyFailClosedState(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredSSHKeyResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertReadKeepsState(t, "ssh_key",
		resp.State.Raw.IsNull(), resp.Diagnostics.HasError())
}

func TestSSHKeyDelete_GatewayNotRouted_DoesNotReportSuccess(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := sshKeySchema(t)
	raw := sshKeyFailClosedState(t)
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredSSHKeyResource(t, server.URL).Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertDeleteDoesNotReportSuccess(t, "ssh_key", resp.Diagnostics.HasError())
}

// The other direction: compute's own flat verdict must still converge.
func TestSSHKeyRead_ServiceVerdict_StillRemovesFromState(t *testing.T) {
	server := newMeAndSSHKeyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.ServiceNotFound(w, "SSH key not found")
	})
	defer server.Close()

	s := sshKeySchema(t)
	raw := sshKeyFailClosedState(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredSSHKeyResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a flat 404 is the service saying it is gone: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a genuine not-found must still drop the resource from state")
	}
}
