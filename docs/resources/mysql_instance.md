---
page_title: "frostmoln_mysql_instance Resource - Frostmoln"
subcategory: ""
description: |-
  Manages a managed MySQL database instance in the Frostmoln platform.
---

# frostmoln_mysql_instance (Resource)

Manages a managed MySQL database instance in the Frostmoln platform.

## Example Usage

```terraform
resource "frostmoln_mysql_instance" "main" {
  name          = "my-mysql-db"
  mysql_version = "8.4"
  flavor        = "db.small"
  storage_gb    = 50
  vpc_id        = frostmoln_vpc.main.id
  subnet_id     = frostmoln_subnet.db.id

  ha_enabled            = true
  backup_enabled        = true
  backup_schedule       = "0 2 * * *"
  backup_retention_days = 7
}

output "mysql_endpoint" {
  value = "${frostmoln_mysql_instance.main.private_ip}:${frostmoln_mysql_instance.main.port}"
}
```

## Argument Reference

The following arguments are supported:

* `name` - (Required) The name of the MySQL instance.
* `mysql_version` - (Required, ForceNew) The MySQL version (e.g. "8.0", "8.4", "9.2").
* `flavor` - (Required) The flavor/size for the database instance (e.g. "db.small", "db.medium").
* `storage_gb` - (Required) The storage size in gigabytes.
* `vpc_id` - (Required, ForceNew) The VPC ID where the database instance will be deployed.
* `subnet_id` - (Required, ForceNew) The subnet ID where the database instance will be deployed.
* `ha_enabled` - (Optional, ForceNew) Whether high availability is enabled with a standby replica.
* `backup_enabled` - (Optional) Whether automated backups are enabled.
* `backup_schedule` - (Optional) Cron expression for the backup schedule (e.g. "0 2 * * *").
* `backup_retention_days` - (Optional) Number of days to retain backups.
* `parameter_group_id` - (Optional) The ID of the parameter group to apply to the instance.

## Attribute Reference

In addition to the arguments above, the following attributes are exported:

* `id` - The unique identifier of the MySQL instance.
* `status` - The current status of the MySQL instance.
* `private_ip` - The private IP address of the MySQL instance.
* `port` - The port number the MySQL instance is listening on (typically 3306).
* `floating_ip` - The floating (public) IP address, if assigned.
* `admin_username` - The admin username for the MySQL instance.
* `created_at` - The timestamp when the instance was created.
* `updated_at` - The timestamp when the instance was last updated.
* `tenant_id` - The tenant ID that owns this instance.

## Import

MySQL instances can be imported using the instance ID:

```shell
terraform import frostmoln_mysql_instance.main <instance-id>
```
