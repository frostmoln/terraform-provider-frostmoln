package workload_identity_binding

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body (tftagstest.RunDirect); a tags change
// rides the tags-only PATCH, never the scopes PUT.
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_workload_identity_binding", func(c *client.Client) resource.Resource {
		return &workloadIdentityBindingResource{client: c}
	})
}
