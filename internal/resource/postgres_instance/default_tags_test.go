package postgres_instance

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// TestDefaultTags drives this resource's own Create, Read and Update against a
// backend that records every request body (tftagstest.RunDirect).
func TestDefaultTags(t *testing.T) {
	tftagstest.RunDirect(t, "frostmoln_postgres_instance", func(c *client.Client) resource.Resource {
		return &postgresInstanceResource{client: c, pollInterval: time.Millisecond, pollTimeout: 5 * time.Second}
	})
}
