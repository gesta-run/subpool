# Codex Account Device Identity Design

Status: Review draft

## Context

Different Codex clients can send different installation identifiers while
sharing one Subpool-managed subscription account. Upstream can therefore see
one account as many devices. Subpool should present one stable device identity
for each Codex subscription account.

This design only normalizes the Codex installation identifier. It is not TLS
fingerprinting, browser emulation, or an attempt to bypass upstream controls.

## Reference research

The design was informed by [`Wei-Shaw/sub2api`](https://github.com/Wei-Shaw/sub2api)
at commit `5097b31457e6dc9f49e5f5c9c72b925ce79543b3`. That project implements broader
account-scoped fingerprint convergence and documents reports of changed quota
behavior when it was enabled by default.

Subpool intentionally adopts only the smallest useful part: a stable
installation ID. Session, thread, turn, window, request, User-Agent, and
transport behavior remain unchanged. Subpool will implement this independently
without copying source code from the LGPL-licensed reference repository.

## Goals

- Give every Codex subscription account one stable installation ID.
- Use the same ID in the direct header and supported metadata projections.
- Keep the ID stable across API keys, processes, replicas, credential refreshes,
  and restarts.
- Recompute the ID for the selected account during retry or failover.
- Require no administrator setting, mode, migration, cache, or background job.
- Preserve existing API keys, client configuration, routing, and SSE behavior.

## Non-goals

- `off`, `balanced`, or other fingerprint modes.
- Session, thread, turn, window, or request-ID convergence.
- TLS, JA3, or JA4 fingerprint imitation.
- User-Agent rotation or official-client impersonation.
- WebSocket or Realtime API support.
- Client detection, allowlists, or a configurable fingerprint rule engine.
- A guarantee about upstream quota, priority, or risk decisions.

## Product behavior

The behavior is automatic and has no console control or public API field.
Every request routed to a Codex subscription account uses that account's stable
installation ID. Requests routed to OpenAI-compatible API-key accounts are not
changed.

Deployment changes the installation ID seen by upstream for existing accounts
on their next request. Client-visible session, thread, turn, window, and
response identifiers remain unchanged. Removing the feature requires a normal
application rollback; there is no runtime mode switch in the first version.

## Identity derivation

`provider_accounts.id` is already a persistent random UUID and is available on
every routing attempt. Subpool derives the installation ID as follows:

1. Hash the versioned domain label
   `subpool/codex/installation-id/v1` and the provider account ID with SHA-256.
2. Use the first 16 digest bytes.
3. Set the RFC 4122 variant and UUIDv4 version bits.
4. Format the result as a canonical lowercase UUID.

The domain label prevents accidental reuse for a future identity type. Hashing
avoids sending Subpool's internal account ID to upstream. The account ID has
enough random entropy, so no additional seed or database column is required.

Consequences:

- All employee API keys routed to the same account share one installation ID.
- Different provider accounts receive different installation IDs.
- Credential refresh and service restart do not change the ID.
- Deleting and recreating an account creates a new ID.
- Failover to another account intentionally changes the ID to that account's
  value.

The derived identifier is internal operational metadata. It must not be exposed
through the control API or included in logs and audit events.

## Request transformation

Transformation happens after account selection and immediately before building
the upstream Codex request. Each retry derives and applies the identity for its
selected account; the downstream request and shared headers remain immutable.

Subpool rewrites only these installation fields:

- HTTP header `X-Codex-Installation-Id` is always set to the derived ID.
- Body `client_metadata["x-codex-installation-id"]` is set to the same ID.
- If `X-Codex-Turn-Metadata` is present as a valid JSON object, its
  `installation_id` field is replaced with the same ID.
- If `client_metadata["x-codex-turn-metadata"]` is present as a valid JSON
  object encoded as a string, its `installation_id` field is replaced with the
  same ID.

An absent `client_metadata` object is created. A present non-object
`client_metadata` value is rejected as an invalid request because it cannot be
updated without discarding client data. Absent turn metadata is not created.
Malformed optional turn metadata is preserved; the canonical direct header and
body field remain authoritative.

All unrelated fields are preserved. In particular, Subpool does not rewrite
`Session-Id`, `Thread-Id`, `X-Codex-Window-Id`, `X-Client-Request-Id`,
`prompt_cache_key`, or their body equivalents.

The existing Codex request-normalization pass should apply streaming rules and
the device identity in one body decode. It runs per upstream attempt because
failover can select a different account. No recursive copy of prompt or tool
payloads is needed.

## Code structure

- `internal/provider/codex/device_identity.go`: deterministic installation-ID
  derivation and targeted metadata rewriting.
- `internal/gateway`: pass the selected provider account ID into each Codex
  attempt and map invalid metadata to the existing invalid-request response.
- `internal/provider/codex/client.go`: set the canonical installation header on
  the outbound request.

No domain-model, store, migration, control API, or web-console change is
required.

## Compatibility and rollout

- Existing API keys, provider credentials, pool bindings, routing decisions,
  session bindings, response IDs, SSE framing, and error codes are unchanged.
- Existing Codex account rows require no backfill.
- The only intentional behavior change is that client-supplied installation IDs
  are replaced by one stable value per selected account.
- OpenAI-compatible accounts and non-Codex upstream requests are unchanged.
- Rollback restores the current pass-through behavior without a data migration.

Roll out the application normally, then observe existing account-health,
upstream-error, and quota signals. Do not add fingerprint-specific analytics or
log identifiers in the first version.

## Failure behavior

- Derivation has no I/O and cannot lose persisted state.
- An empty provider account ID is an internal configuration error;
  fail before contacting upstream.
- A non-object `client_metadata` value returns the existing invalid-request
  response.
- Retry with refreshed credentials keeps the same installation ID.
- Failover recomputes the ID for the newly selected account.
- Errors never include client-supplied or derived identifiers.

## Verification

- Unit tests cover deterministic derivation, account separation, UUID format,
  and domain versioning.
- Transformation tests cover the header, flat body metadata, nested turn
  metadata, absent metadata, malformed optional metadata, preservation of
  unrelated fields, and rejection of non-object `client_metadata`.
- Gateway tests cover credential refresh stability and account failover.
- Regression tests prove session, thread, turn, window, request, cache, and SSE
  behavior are unchanged.

## Senior architect and product review

### High-confidence problems and risks

- Upstream policy is opaque. Stable device identity must not be presented as a
  quota improvement or risk-control bypass.
- Existing accounts change device identity immediately after deployment. With
  no runtime switch, rollback is the only fast escape if upstream behavior
  regresses.
- Header and body projections can diverge if rewritten in separate paths. One
  derived value must be passed through the entire attempt.
- Failover must not reuse the failed account's ID.
- Malformed metadata needs explicit behavior; silently replacing the whole
  object could delete client data.

### Over-engineering to avoid

- A mode column, per-account setting, seed column, migration, admin UI, session
  table, cache, worker, and analytics are unnecessary for account-level device
  stability.
- Rewriting session, thread, turn, window, request, or cache identifiers expands
  both compatibility risk and upstream uncertainty without serving this scope.
- A generic fingerprint framework is premature; one small Codex-specific
  transformer is sufficient.

### Smaller version assessment

This is the smaller viable version: derive one installation ID from the
existing provider account ID and rewrite only installation fields. Removing
metadata consistency would be smaller in code but could send contradictory
device identities, so it is not recommended. No additional product mode or
storage is justified.
