package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccFullStack_basic(t *testing.T) {
	imageName := TestImageName()
	flavorName := TestFlavorName()
	sshKeyName := RandomName("sshkey")
	vpcName := RandomName("vpc")
	subnetName := RandomName("subnet")
	sgName := RandomName("sg")
	instanceName := RandomName("instance")
	volumeName := RandomName("volume")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { TestAccPreCheck(t) },
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		CheckDestroy:             nil,
		Steps: []resource.TestStep{
			{
				Config: testAccFullStackConfig(imageName, flavorName, sshKeyName, vpcName, subnetName, sgName, instanceName, volumeName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_instance.test", "status", "running"),
					resource.TestCheckResourceAttrSet("frostmoln_volume.test", "status"),
					resource.TestCheckResourceAttrSet("frostmoln_floating_ip.test", "instance_id"),
					resource.TestCheckResourceAttrSet("frostmoln_floating_ip.test", "address"),
				),
			},
		},
	})
}

func testAccFullStackConfig(imageName, flavorName, sshKeyName, vpcName, subnetName, sgName, instanceName, volumeName string) string {
	return fmt.Sprintf(`
data "frostmoln_image" "test" {
  name = %[1]q
}

data "frostmoln_flavor" "test" {
  name = %[2]q
}

resource "frostmoln_ssh_key" "test" {
  name       = %[3]q
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGwf2tOoGfmCSSeKYxFSS/JRbMbz5BJ5A9xVyXc5Oc/n acctest@test"
}

resource "frostmoln_vpc" "test" {
  name = %[4]q
  cidr = "10.200.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = %[5]q
  cidr   = "10.200.1.0/24"
  vpc_id = frostmoln_vpc.test.id
}

resource "frostmoln_security_group" "test" {
  name = %[6]q
}

resource "frostmoln_security_group_rule" "ssh" {
  security_group_id = frostmoln_security_group.test.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_cidr       = "0.0.0.0/0"
}

resource "frostmoln_instance" "test" {
  name      = %[7]q
  image_id  = data.frostmoln_image.test.id
  flavor_id = data.frostmoln_flavor.test.id
  vpc_id    = frostmoln_vpc.test.id
  subnet_id = frostmoln_subnet.test.id

  security_groups = [frostmoln_security_group.test.id]
  ssh_key_names   = [frostmoln_ssh_key.test.name]
}

resource "frostmoln_volume" "test" {
  name    = %[8]q
  size_gb = 10
}

resource "frostmoln_volume_attachment" "test" {
  volume_id   = frostmoln_volume.test.id
  instance_id = frostmoln_instance.test.id
}

resource "frostmoln_floating_ip" "test" {
  instance_id = frostmoln_instance.test.id
}
`, imageName, flavorName, sshKeyName, vpcName, subnetName, sgName, instanceName, volumeName)
}
