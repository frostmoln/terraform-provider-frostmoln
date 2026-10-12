package application_gateway

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body (tftagstest.RunDirect): provider
// default_tags reach the create, a resource key wins, a server-stamped key lands
// in tags_all only, a default_tags change keeps the keys the provider does not
// manage, and a clear clears.
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_application_gateway", func(c *client.Client) resource.Resource {
		return &gatewayResource{client: c, pollInterval: time.Millisecond, createTimeout: 5 * time.Second, deleteTimeout: 5 * time.Second}
	})
}
