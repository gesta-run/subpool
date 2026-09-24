# Code Review Remediation Plan

## Goal

Resolve the confirmed runtime, scalability, maintainability, and frontend consistency issues found during the September 2026 review without changing provider routing semantics, stored data, or existing authentication behavior.

## Scope

### 1. Runtime correctness

- Replace the unsynchronized Codex app-server stderr buffer with a concurrency-safe buffer.
- Add a race regression test for cancellation while stderr is still being copied.
- Install `tini` in the runtime image and make it the image entrypoint so every container deployment reaps orphaned Codex descendants.
- Remove the pre-only Docker init override after the image-level init is verified, avoiding nested init processes.

### 2. Frontend request consistency

- Give list loaders an abort signal and monotonically increasing request ID.
- Abort superseded requests and ignore responses that are no longer current.
- Preserve the existing loading, empty, and error states.

### 3. Database concurrency and query safety

- Replace the global account-assignment advisory lock with a lock derived from the pool ID.
- Keep row-level account locking and the existing capacity checks.
- Remove the global deletion lock and rely on row locks plus foreign-key constraints.
- Introduce `sqlc` and move production queries out of Go source files into named `.sql` query files.
- Keep migrations as numbered handwritten SQL; migrations are the intentional exception because their exact historical text is part of the schema history.

### 4. Usage scalability

- Add a server-aggregated usage summary query grouped by API key and model.
- Add cursor pagination with a bounded page size.
- Move the console to the paginated summary API so it never loads all historical daily rows into browser memory.
- Keep date filtering and all-time reporting.

Compatibility decision:

- Replace `GET /api/v1/usage` with the bounded, paginated summary contract.
- Do not preserve the old unpaginated response contract.
- Preserve all existing historical usage rows and include them in the new aggregated results.

### 5. Structure and dead code

- Extract Responses WebSocket session logic into `internal/gateway/responsesws` behind a narrow backend interface.
- Keep HTTP routing and shared account-selection behavior in `internal/gateway`.
- Split tests by transport and behavior so no test file remains a feature-wide catch-all.
- Remove the unused `gatewayError.Error` method and `testIDToken` helper.
- Decode credentials once according to provider type instead of decoding OpenAI-compatible credentials through the Codex type first.

## Compatibility

- No database columns are removed or rewritten.
- Existing credentials and API keys remain valid.
- Provider selection, session pinning, failover limits, and response formats remain unchanged.
- The image entrypoint still forwards arguments and termination signals to Subpool through `tini`.
- WebSocket extraction is package-only refactoring guarded by the existing HTTP bridge, native transport, continuation, failover, and shutdown tests.

## Performance Expectations

- Account assignments in different pools no longer block each other.
- Usage pages read bounded aggregated pages instead of unbounded daily rows.
- Generated queries retain prepared-statement-friendly parameters and compile-time row scanning.
- Request cancellation prevents stale frontend fetches from consuming work or overwriting newer state.

## Delivery

Use four ready-for-review pull requests, each containing exactly one signed-off commit:

1. Runtime correctness and frontend request ordering.
2. `sqlc` migration and per-pool locking.
3. Paginated usage summary API and console migration.
4. WebSocket package extraction and dead-code cleanup.

Merge in that order. Run the full build, normal tests, race tests, and Compose validation before each pull request. Deploy only from `main` to pre.

## Acceptance Criteria

- `go test -race ./...` passes.
- Repeated Codex app-server cancellation leaves no zombie processes in a container started directly from the image.
- Concurrent assignments in different pools proceed independently while capacity remains correct within one pool.
- Usage pages remain bounded for large historical datasets.
- Existing gateway and control API tests pass without response-shape regressions.
- No production Go file contains handwritten SQL after the query migration.
- No production function exceeds 100 lines and no production source file exceeds 1,000 lines.
