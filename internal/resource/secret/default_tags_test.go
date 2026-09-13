package secret

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body: provider default_tags reach the
// create, a resource key wins, a server-stamped key lands in tags_all only, a
// default_tags change keeps the keys the provider does not manage, and a clear
// clears. The removal of a default applied earlier needs private state; the
// provider-wide gate (TestDefaultTagsContract) asserts it through the protocol.
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_secret", func(c *client.Client) resource.Resource {
		return &secretResource{client: c}
	})
}
