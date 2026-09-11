package kubernetes_node_pool

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

func TestGetPollDefaults(t *testing.T) {
	r := &kubernetesNodePoolResource{}
	if r.getPollInterval() != 10*time.Second {
		t.Errorf("expected default poll interval 10s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 30*time.Minute {
		t.Errorf("expected default poll timeout 30m, got %v", r.getPollTimeout())
	}
}

// TestResolveBudgetsDefaultsPinTodaysConstants pins the timeouts block's
// fallback: with no block configured, every verb budgets at the value this
// resource has always hardcoded, and a test's pollTimeout injection still
// shrinks the default (the resolveBudgets seam keeps the harness working).
func TestResolveBudgetsDefaultsPinTodaysConstants(t *testing.T) {
	bare := (&kubernetesNodePoolResource{}).resolveBudgets(nil)
	if want := timeouts.Uniform(30 * time.Minute); bare != want {
		t.Errorf("resolveBudgets(nil) = %+v, want %+v", bare, want)
	}

	r := &kubernetesNodePoolResource{pollTimeout: time.Second}
	if got := r.resolveBudgets(nil); got != timeouts.Uniform(time.Second) {
		t.Errorf("an injected pollTimeout must stay the default budget, got %+v", got)
	}
}

// TestTimeoutsBlockInSchema checks the schema carries the customer-tunable
// timeouts block with its three optional verbs.
func TestTimeoutsBlockInSchema(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	b, ok := schemaResp.Schema.Blocks["timeouts"]
	if !ok {
		t.Fatal("expected the timeouts block in the schema")
	}
	nested, ok := b.(schema.SingleNestedBlock)
	if !ok {
		t.Fatalf("timeouts must be a single nested block, got %T", b)
	}
	for _, verb := range []string{"create", "update", "delete"} {
		attr, ok := nested.Attributes[verb]
		if !ok {
			t.Errorf("expected the %s attribute in the timeouts block", verb)
			continue
		}
		if !attr.IsOptional() {
			t.Errorf("timeouts.%s must be optional", verb)
		}
	}
}
