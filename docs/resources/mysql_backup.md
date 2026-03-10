---
page_title: "frostmoln_mysql_backup Resource - Frostmoln"
subcategory: ""
description: |-
  Manages a backup of a managed MySQL instance. Backups are immutable after creation.
---

# frostmoln_mysql_backup (Resource)

Manages a backup of a managed MySQL instance. Backups are immutable after creation.

## Example Usage

```terraform
resource "frostmoln_mysql_backup" "daily" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "daily-backup"
  type        = "full"
}

resource "frostmoln_mysql_backup" "binlog" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "binlog-backup"
  type        = "binlog"
}
```

## Argument Reference

The following arguments are supported:

* `instance_id` - (Required, ForceNew) The ID of the MySQL instance to back up.
* `name` - (Required, ForceNew) The name of the backup.
* `type` - (Optional, ForceNew) The type of backup: "full", "incremental", or "binlog". Defaults to "full".

## Attribute Reference

In addition to the arguments above, the following attributes are exported:

* `id` - The unique identifier of the backup.
* `status` - The current status of the backup.
* `size_bytes` - The size of the backup in bytes.
* `started_at` - The timestamp when the backup started.
* `completed_at` - The timestamp when the backup completed.

## Import

MySQL backups can be imported using the backup ID:

```shell
terraform import frostmoln_mysql_backup.daily <backup-id>
```
