package snapshot_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccSnapshot_basic(t *testing.T) {
	volumeName := acctest.RandomName("snapvol")
	snapshotName := acctest.RandomName("snap")
	resourceName := "frostmoln_snapshot.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_snapshot", "/snapshots"),
		Steps: []resource.TestStep{
			{
				Config: testAccSnapshotConfig(volumeName, snapshotName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", snapshotName),
					resource.TestCheckResourceAttrPair(resourceName, "volume_id", "frostmoln_volume.test", "id"),
				),
			},
		},
	})
}

func testAccSnapshotConfig(volumeName, snapshotName string) string {
	return fmt.Sprintf(`
resource "frostmoln_volume" "test" {
  name    = %q
  size_gb = 10
}

resource "frostmoln_snapshot" "test" {
  name      = %q
  volume_id = frostmoln_volume.test.id
}
`, volumeName, snapshotName)
}
