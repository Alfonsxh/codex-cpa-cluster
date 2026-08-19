# ADR 0001: Stable Edge with blue/green Gateway slots

- Status: Accepted
- Date: 2026-08-13

## Context

The legacy Gateway owned host port `18317` and also contained Portal/Dashboard routes. Replacing
Admin/static/Gateway source through one deployment boundary could recreate the public process and
interrupt existing API Key clients, including long-running Codex streaming requests.

The required invariant is narrower than “all releases are zero downtime”: ordinary Admin and Web
changes must not touch the `/v1` data plane, and Gateway changes must be validated before receiving
new traffic while existing connections can finish.

## Decision

Introduce four independently versioned runtime components:

1. Stable `edge` owns host ports and routes UI paths to Web and API paths to the active Gateway.
2. `web` owns Portal/Dashboard files and bounded Admin/self-service proxy paths.
3. Identical `gateway-blue`/`gateway-green` slots own API Key auth, quota and CPA routing only.
4. `admin` owns management/control/usage processes only.

`state/edge/active-gateway.conf`, written only by `scripts/edge_slot.py`, selects the active slot and
is included by its fixed filename. Gateway delivery validates the inactive slot's internal route
and real public auth/quota path, atomically switches the file, gracefully reloads Edge, checks the
public path, drains the old slot, and stops it only after zero in-flight requests. Rollback switches
back first and stops the new slot only when its counter is already zero; otherwise it is preserved
for manual inspection.

## Consequences

- Admin/Web updates leave Edge and active Gateway unchanged.
- Gateway updates have a pre-traffic validation and rollback boundary.
- Existing Gateway connections can drain after new traffic switches.
- Edge itself becomes a small high-stability component; changing its image/service can still affect
  traffic and should be scheduled deliberately.
- The first legacy migration requires a maintenance window because the old Gateway and new Edge
  cannot bind the same host port concurrently.
- Two Gateway service definitions/images require temporary disk capacity, while only one slot is
  required in steady state.

## Alternatives considered

- Keep one Gateway and use only `openresty -s reload`: insufficient for image/Lua runtime changes
  and still couples static/control updates to the public process.
- Run both Gateways with separately published host ports and switch external TLS: makes deployment
  dependent on an external proxy contract not owned by this repository.
- Route API traffic through Admin/Web: adds control-plane availability and latency to every request
  and violates the required isolation.

## Evidence

`docker-compose.yml`, `edge/`, `web/`, `gateway/`, `scripts/deploy-release.sh`,
`scripts/edge_slot.py`, `scripts/gateway_release_probe.py`, `tests/test_runtime_boundaries.py`, and
`tests/test_edge_slot.py`.
