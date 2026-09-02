# Provider Account Health Design

Status: proposed

## Goal

Make provider availability visible and truthful without breaking existing pools or turning health probes into a new source of provider traffic.

## Current behavior

- A newly connected account is stored with operational status `active` without an upstream check.
- The console treats `active` as healthy.
- Real traffic changes status after selected responses: success marks the account active, `401` marks authentication failed, and `429` starts cooldown.
- There is no manual connection check, scheduled probe, or persisted last-check result.

## State model

Operational state and observed health are separate concepts.

### Operational status

Keep the existing `status` field and values for API and database compatibility:

- `active`: enabled for routing
- `disabled`: disabled by an administrator
- `cooling_down`: temporarily excluded after rate limiting
- `auth_failed`: excluded after definitive authentication failure
- `exhausted`: reserved for provider quota handling

The console labels `active` as **Enabled**, not Healthy.

### Health status

Add an independent `health_status` field:

- `unknown`: never checked or the provider does not expose a safe probe
- `healthy`: the latest probe or real request succeeded
- `unhealthy`: repeated transport/server failures or a definitive authentication failure

Add these columns to `provider_accounts`:

- `health_status text NOT NULL DEFAULT 'unknown'`
- `last_checked_at timestamptz`
- `last_health_error_code text`
- `consecutive_health_failures integer NOT NULL DEFAULT 0`
- `next_health_check_at timestamptz`

Store only stable error codes such as `authentication_failed`, `timeout`, `connection_failed`, `provider_5xx`, and `probe_unsupported`. Do not store upstream response bodies, URLs, API keys, prompts, or raw error messages.

## Check behavior

### On account creation

- Codex OAuth: use the existing authenticated usage request as the initial check.
- OpenAI-compatible API: call `GET {base_url}/models` with the configured bearer key.
- A `2xx` response marks the account healthy.
- `401` or `403` rejects creation as a definitive credential failure.
- `429` proves the endpoint and credential are reachable; save the account as healthy and apply cooldown.
- Timeout, network failure, `404`, or `405` does not block creation. Save the account with health `unknown` and return a warning because some compatible providers do not implement `/models`.

The creation check has a five-second timeout.

### Manual check

Add:

`POST /api/v1/provider-accounts/{id}/check`

The endpoint returns the persisted health result and never returns decrypted credentials or a raw upstream body. The Accounts page exposes a **Check connection** action for every provider type.

### Real traffic feedback

- Any successful provider response marks health healthy and resets consecutive failures.
- `401` or `403` marks health unhealthy immediately and retains the existing authentication-failed routing behavior.
- `429` updates operational cooldown but keeps health healthy because the endpoint is reachable.
- Timeout, connection failure, or `5xx` increments consecutive failures.
- Mark health unhealthy after three consecutive failures to avoid flapping on transient incidents.

## Scheduled checks

Run a lightweight worker in the Subpool process:

- Default interval: five minutes with per-account jitter.
- Check only enabled accounts that have not had a successful real request since their previous due time.
- Claim at most 50 due accounts per database batch.
- Maximum concurrent probes: eight.
- Probe timeout: five seconds.
- Index `next_health_check_at` for bounded due-account queries.
- Use PostgreSQL row claiming with `FOR UPDATE SKIP LOCKED` so the design remains safe if multi-replica support is added later.

Do not create a health-history table in this phase. Updating one row per account keeps storage bounded and avoids unbounded monitoring data.

## Routing compatibility

Routing policy:

- Existing accounts migrate to health `unknown`.
- Existing `active` accounts remain routable while unknown.
- Accounts marked `unhealthy` are excluded from new assignments and failover candidates.
- Definitive auth failures exclude accounts immediately.

This preserves compatibility after migration because existing accounts start as `unknown`, while preventing a confirmed unhealthy account from continuing to receive new work.

## Console changes

- Rename summary metric **Healthy** to show only `health_status=healthy`.
- Show separate **State** and **Health** columns.
- Health labels: Healthy, Unchecked, Unhealthy, Checking.
- Show `last_checked_at` and a safe error description.
- Keep OAuth refresh separate from connection checking.
- Disable repeated manual checks while one is running and expose accessible loading and error feedback.

## API compatibility

- Existing fields and status values remain unchanged.
- Provider account responses add `health_status`, `last_checked_at`, and `last_health_error_code`.
- Old clients continue to deserialize responses because changes are additive.

## Test scope

- Migration defaults existing accounts to unknown without changing routing status.
- Creation checks for success, auth failure, unsupported probe, timeout, and rate limiting.
- Manual check never exposes secrets or upstream response bodies.
- Real requests update health and apply the three-failure threshold.
- Scheduler batching, concurrency, jitter, and PostgreSQL claim behavior.
- Console state labels, keyboard actions, loading state, and summary counts.
