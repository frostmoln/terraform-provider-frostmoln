package s3_credential

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body (tftagstest.RunDirect): a tags change
// is a PUT carrying tags alone, never a replacement.
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_s3_credential", func(c *client.Client) resource.Resource {
		return &s3CredentialResource{client: c}
	})
}
