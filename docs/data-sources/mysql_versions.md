---
page_title: "frostmoln_mysql_versions Data Source - Frostmoln"
subcategory: ""
description: |-
  Lists available MySQL versions for managed database instances.
---

# frostmoln_mysql_versions (Data Source)

Lists available MySQL versions for managed database instances.

## Example Usage

```terraform
data "frostmoln_mysql_versions" "available" {}

output "mysql_versions" {
  value = data.frostmoln_mysql_versions.available.versions
}
```

## Attribute Reference

The following attributes are exported:

* `versions` - A list of available MySQL versions. Each version has:
  * `version` - The MySQL version string (e.g. "8.0", "8.4", "9.2").
  * `status` - The support status (e.g. "supported", "deprecated").
  * `end_of_life` - The end-of-life date for this version, if known.
