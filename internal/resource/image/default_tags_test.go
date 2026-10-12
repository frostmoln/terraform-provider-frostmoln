package image

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create (the full upload and import
// flow), Read and Update against a backend that records every request body
// (tftagstest.RunDirect): a tags change is a PUT carrying tags alone, never a
// replacement of the image.
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_image", func(c *client.Client) resource.Resource {
		return &imageResource{client: c, pollInterval: time.Millisecond}
	})
}
