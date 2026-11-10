# Fact target identity: host → host:port (service)

## New data

New `js_interface` facts and observed `record_fact` facts carry:

```json
{"host":"127.0.0.1:3000","service":"127.0.0.1:3000"}
```

- `host` is now a service identity (`host:port`), not a bare hostname.
- Static extraction (`extract_js`) is scoped to a service URL and uses the
  service's host:port in the fact key. Dynamic extraction does the same for the
  page target.
- `record_fact` derives the fact target from its `asset_ids` (or the producing
  intent's `asset_ids`) and writes the same `host`/`service` fields.
- The interface dedupe key is `jsif|<host:port>|<method>|<path>`, so
  `127.0.0.1:3000/api/Users` and `127.0.0.1:8081/api/Users` are different facts.

`graph_overview.js_intel` exposes both dimensions:

- `interfaces_by_host` — host-only rollup (e.g. `127.0.0.1`: 52)
- `interfaces_by_service` — service rollup (e.g. `127.0.0.1:3000`: 52,
  `127.0.0.1:8081`: N)

## Legacy data

Old facts with a bare `host` or an empty `host` are left untouched. They remain
readable and are rendered by `interfaces_by_host`; they cannot be safely
backfilled with a port because the old payload did not record one. New runs use
the new identity; if a legacy deployment needs a clean split, re-run the target
or start a fresh task. No destructive migration is performed.
