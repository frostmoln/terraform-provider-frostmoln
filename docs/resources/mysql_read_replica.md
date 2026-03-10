---
page_title: "frostmoln_mysql_read_replica Resource - Frostmoln"
subcategory: ""
description: |-
  Manages a read replica of a managed MySQL instance. Read replicas are immutable after creation.
---

# frostmoln_mysql_read_replica (Resource)

Manages a read replica of a managed MySQL instance. Read replicas are immutable after creation.

## Example Usage

```terraform
resource "frostmoln_mysql_read_replica" "reader" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "mysql-reader-1"
}

output "mysql_reader_endpoint" {
  value = "${frostmoln_mysql_read_replica.reader.private_ip}:${frostmoln_mysql_read_replica.reader.port}"
}
```

## Argument Reference

The following arguments are supported:

* `instance_id` - (Required, ForceNew) The ID of the primary MySQL instance to replicate.
* `name` - (Required, ForceNew) The name of the read replica.

## Attribute Reference

In addition to the arguments above, the following attributes are exported:

* `id` - The unique identifier of the read replica.
* `status` - The current status of the read replica.
* `private_ip` - The private IP address of the read replica.
* `port` - The port number the read replica is listening on (typically 3306).
* `replication_lag_bytes` - The replication lag in bytes between primary and replica.

## Import

MySQL read replicas can be imported using the replica ID:

```shell
terraform import frostmoln_mysql_read_replica.reader <replica-id>
```
