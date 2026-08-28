package container_registry_credential

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/failclosed"
)

// container_registry_credential is the REGISTRY-backed instance of the
// fail-closed contract. See internal/failclosed for the rule, and for why the
// ordinary not-found fixtures cannot pin it.
//
// It matters more here than almost anywhere: dropping a credential from state on
// a gateway misroute discards the only copy of its secret that exists.

func TestCredentialRead_GatewayNotRouted_KeepsState(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertReadKeepsState(t, "container_registry_credential",
		resp.State.Raw.IsNull(), resp.Diagnostics.HasError())
}

func TestCredentialDelete_GatewayNotRouted_DoesNotReportSuccess(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertDeleteDoesNotReportSuccess(t, "container_registry_credential",
		resp.Diagnostics.HasError())
}

// The other direction: the registry's own flat verdict must still converge.
func TestCredentialRead_ServiceVerdict_StillRemovesFromState(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.ServiceNotFound(w, "credential not found")
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a flat 404 is the service saying it is gone: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a genuine not-found must still drop the resource from state")
	}
}

// REGISTRY_NOT_ENABLED is a statement about the REGISTRY, not about this
// credential — so it must behave like the gateway misroute above, not like a
// 404. The stakes are higher than the usual fail-closed case: what state holds
// is the only copy of the secret, and nothing can reissue it.

func registryNotEnabled(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w,
		`{"code":"invalid_state","message":"this tenant's registry is not enabled",`+
			`"details":{"reason":"REGISTRY_NOT_ENABLED"}}`)
}

func TestCredentialRead_RegistryNotEnabled_KeepsState(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		registryNotEnabled(w)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.State.Raw.IsNull() {
		t.Fatal("state was dropped on a registry-level refusal; the secret it held is unrecoverable")
	}
	if !resp.Diagnostics.HasError() {
		t.Error("the refusal was swallowed silently — the practitioner gets no signal")
	}
}

func TestCredentialDelete_RegistryNotEnabled_DoesNotReportSuccess(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		registryNotEnabled(w)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	// The credential is still live at the endpoint; a clean destroy would claim
	// otherwise and the practitioner would never look again.
	failclosed.AssertDeleteDoesNotReportSuccess(t, "container_registry_credential",
		resp.Diagnostics.HasError())
}
