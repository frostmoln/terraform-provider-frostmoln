package snapshot

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body — its tags change in place since
// storage v1.23.0 (PUT on the snapshot, a metadata REPLACE), so a tag or
// default_tags change never replaces a snapshot. Provider default_tags reach the
// create, a resource key wins, a server-stamped key lands in tags_all only, a
// default_tags change keeps the keys the provider does not manage, and a clear
// clears. The removal of a default applied earlier needs private state; the
// provider-wide gate (TestDefaultTagsContract) asserts it through the protocol.
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_snapshot", func(c *client.Client) resource.Resource {
		return &snapshotResource{client: c}
	})
}
