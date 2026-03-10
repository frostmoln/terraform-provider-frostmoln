---
page_title: "frostmoln_database_engines Data Source - Frostmoln"
subcategory: ""
description: |-
  Lists all available database engines for managed database instances.
---

# frostmoln_database_engines (Data Source)

Lists all available database engines for managed database instances.

## Example Usage

```terraform
data "frostmoln_database_engines" "available" {}

output "supported_engines" {
  value = data.frostmoln_database_engines.available.engines
}
```

## Attribute Reference

The following attributes are exported:

* `engines` - A list of available database engines. Each engine has:
  * `name` - The engine name (e.g. "postgresql", "mysql").
  * `description` - A human-readable description of the engine.
  * `versions` - A list of supported version strings for this engine.
