package api_key

import (
	"context"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/failclosed"
)

// The end-to-end half of the fail-closed contract. A unit test on IsNotFound is
// not enough: what matters is what a RESOURCE does when an unrouted-path 404
// reaches it, because the answer used to be "destroy the customer's state".
//
// The api-gateway answers 404 NESTED for any path it does not route. Before the
// FlatEnvelope gate, that reached Read as "the resource is gone" (state removed
// on a routine refresh) and Delete as "already deleted" (`terraform destroy`
// printing success while the key stayed live). Both must now be loud errors.
//
// api_key is the representative case because it is identity-backed — one of the
// four resources the producer half of this change was needed for. See ADR-0117.

func apiKeyStateValue(t *testing.T) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(apiKeyTFType(t), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "ak-123"),
		"name":        tftypes.NewValue(tftypes.String, "key"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"scopes":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"expires_at":  tftypes.NewValue(tftypes.String, nil),
		"rate_limit":  tftypes.NewValue(tftypes.Number, nil),
		"key":         tftypes.NewValue(tftypes.String, "saved"), // pragma: allowlist secret
		"key_prefix":  tftypes.NewValue(tftypes.String, "fmk"),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})
}

func TestAPIKeyRead_GatewayNotRouted_KeepsStateAndErrors(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := apiKeySchema(t)
	raw := apiKeyStateValue(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredAPIKeyResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertReadKeepsState(t, "api_key",
		resp.State.Raw.IsNull(), resp.Diagnostics.HasError())
}

func TestAPIKeyDelete_GatewayNotRouted_DoesNotReportSuccess(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := apiKeySchema(t)
	raw := apiKeyStateValue(t)
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredAPIKeyResource(t, server.URL).Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertDeleteDoesNotReportSuccess(t, "api_key", resp.Diagnostics.HasError())
}

// The other half: a genuine service verdict must still work, or the fix has
// simply traded a silent data loss for a permanent inability to converge.
func TestAPIKeyRead_ServiceVerdict_StillRemovesFromState(t *testing.T) {
	server := apiKeyMeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.ServiceNotFound(w, "api key not found")
	})
	defer server.Close()

	s := apiKeySchema(t)
	raw := apiKeyStateValue(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredAPIKeyResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a flat 404 is the service saying it is gone: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a genuine not-found must still drop the resource from state")
	}
}
