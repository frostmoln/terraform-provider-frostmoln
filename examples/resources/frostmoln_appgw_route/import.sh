# A route is imported by its gateway, its listener and its own id.
terraform import frostmoln_appgw_route.api <gateway_id>/<listener_id>/<route_id>

# WARNING: header VALUES may not be importable. They are secrets, and once the
# platform returns only the header names, `request_headers_set` and
# `response_headers_set` arrive with their names but without their values. A
# route that sets headers then plans one REPLACE on the first apply after the
# import, which re-creates it with the values in your configuration; the
# provider warns when this happens. A route that sets no headers imports
# cleanly. To hold the replace off, add
# `lifecycle { ignore_changes = [request_headers_set, response_headers_set] }`
# until you can take it.
