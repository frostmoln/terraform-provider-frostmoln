package public_ip

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The gateway-bound refusal translation is now CODE-BASED: the typed
// OperationError's errorCode is the stable predicate (prose is rewordable
// copy). The literal arm below is the legacy fallback for operations that
// failed before provisioning's allow-list covered the code.

func TestOperationErrorRefusalTranslatesOnTheTypedCode(t *testing.T) {
	var d diag.Diagnostics
	AddOperationError(&d, "Fallback Summary", &client.OperationError{
		OperationID: "op-1",
		Status:      "failed",
		ErrorCode:   errCodeInUseByGateway,
		Message:     "public IP is the outbound source address of vpc-1 (prose may drift)",
	})
	if !d.HasError() {
		t.Fatal("expected the translated refusal")
	}
	if d.Errors()[0].Summary() != inUseByGatewaySummary {
		t.Fatalf("the typed code must translate, got summary %q", d.Errors()[0].Summary())
	}
}

func TestOperationErrorRefusalTranslatesOnTheLegacyProse(t *testing.T) {
	var d diag.Diagnostics
	AddOperationError(&d, "Fallback Summary", errors.New(
		"operation op-1 failed: PUBLIC_IP_IN_USE_BY_GATEWAY: public IP is the outbound source address of vpc-1",
	))
	if !d.HasError() {
		t.Fatal("expected the translated refusal")
	}
	if d.Errors()[0].Summary() != inUseByGatewaySummary {
		t.Fatalf("the legacy code-prefixed prose must still translate, got %q", d.Errors()[0].Summary())
	}
}

func TestOperationErrorWithoutAMatchCarriesFallbackAndProse(t *testing.T) {
	var d diag.Diagnostics
	AddOperationError(&d, "Fallback Summary", &client.OperationError{ErrorCode: "other", Message: "nothing was created"})
	if !d.HasError() || d.Errors()[0].Summary() != "Fallback Summary" {
		t.Fatalf("untranslated refusals keep the fallback summary, got %+v", d)
	}
	if !strings.Contains(d.Errors()[0].Detail(), "nothing was created") {
		t.Errorf("the prose must ride verbatim, got: %s", d.Errors()[0].Detail())
	}
}
