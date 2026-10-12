package api_key

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body (tftagstest.RunDirect).
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_api_key", func(c *client.Client) resource.Resource {
		return &apiKeyResource{client: c}
	})
}
