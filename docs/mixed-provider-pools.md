# Mixed-provider pools

## Goal

Allow one employee API key to use subscription accounts as the primary capacity and OpenAI-compatible API accounts as paid fallback capacity in the same pool.

The first implementation must preserve existing pools, API keys, routing behavior, and database records unless an administrator explicitly adds a different provider type to a pool.

## User-visible behavior

- A pool may contain Codex subscription accounts and OpenAI-compatible API accounts.
- Subscription credentials are primary capacity; API-key credentials are fallback capacity.
- New requests use healthy primary accounts first.
- Retryable primary failures may move to another subscription account and then to paid API fallback.
- After paid fallback, the next new request checks subscription capacity again instead of staying on paid API indefinitely.
- Continuation requests remain pinned to their original account and never switch providers.
- The same downstream model name is forwarded to every provider. Administrators must configure fallback endpoints that support the requested models.
- Existing homogeneous pools continue to behave exactly as they do today.

## Compatibility

The recommended compatible change is additive:

- Keep `pools.provider` for existing API consumers.
- Add `mixed` as a valid provider value.
- Existing `codex` and `openai_compatible` pools remain unchanged.
- A homogeneous pool becomes `mixed` only after an administrator attaches another provider type.
- Existing employee API keys remain valid and keep their current account binding until normal failover occurs.

Strict clients that model `pool.provider` as a closed enum must accept the new `mixed` value.

## Routing policy

Use the existing `pool_accounts.priority` column as the routing tier:

| Credential type | Default priority | Role |
| --- | ---: | --- |
| Subscription OAuth | `0` | Primary |
| Provider API key | `100` | Paid fallback |

Account selection changes from assignment-count-first to priority-first:

1. Lowest membership priority.
2. Lowest active employee-key assignment count normalized by membership weight.
3. Random selection between otherwise equal accounts.

For a new, non-continuation request, the gateway tracks accounts already attempted and tries eligible accounts in priority order. Attempts are bounded to eight accounts per request to limit latency and upstream amplification. Failover covers definitive authentication failure, HTTP `429`, transport failures, and upstream `5xx` responses. Other upstream `4xx` responses are returned directly. Continuation requests remain pinned to their original account and never switch providers.

## Data model and migration

Add a migration that extends the `pools.provider` check constraint to:

```text
codex | openai_compatible | mixed
```

No new table or Redis dependency is required.

When creating or updating membership:

- One provider type: store that provider on the pool.
- More than one provider type: store `mixed`.
- Membership priority defaults from the account credential type.

## Control API

- Pool creation accepts account IDs from multiple providers.
- Adding a pool account no longer requires `pool.provider = account.provider`.
- Membership insertion and pool-provider recomputation happen in one transaction.
- Pool responses may return `provider: "mixed"`.
- No existing endpoint or request field is removed.

## Console

- Remove the single-provider restriction from the account picker.
- Show provider and credential type for every candidate.
- Label subscription accounts as primary and API-key accounts as fallback.
- Replace the misleading empty state with `No additional active accounts are available.`
- Display mixed pools as `mixed providers`.

The first version does not add manual priority editing. Automatic subscription-first behavior is sufficient for the stated fallback use case.

## Performance and safety

- Account selection remains a PostgreSQL transaction and indexed pool-membership query.
- The eight-attempt ceiling prevents unbounded request fan-out when a large pool is unhealthy.
- Existing advisory locking continues to serialize employee-key reassignment.
- No request body, prompt, response, or source code is persisted.
- Horizontal multi-replica coordination remains out of scope.

## Acceptance tests

1. Existing Codex-only and API-only pools load without migration changes to their provider value.
2. Creating a mixed pool returns `provider: "mixed"`.
3. Attaching an API account to a Codex pool changes the pool to `mixed`.
4. Existing employee API keys remain valid after a pool becomes mixed.
5. Healthy subscription accounts are selected before API-key accounts.
6. After retryable subscription failures, a compatible API account can complete the same new request.
7. Continuation requests never change their bound account.
8. Non-retryable model or validation errors do not consume fallback capacity.
9. The gateway never attempts more than eight accounts for one request.
10. The console lists active accounts from every provider and explains primary versus fallback roles.
11. A key temporarily bound to paid fallback returns to a healthy subscription account on its next new request.

## Senior architecture and product review

High-confidence problems in the current implementation are the pool-level provider constraint, the matching frontend filter, assignment-count-first routing, the two-attempt retry ceiling, and a misleading empty-state message.

Potential over-engineering to avoid in this release includes model alias maps, arbitrary routing graphs, per-request cost optimization, manual priority editors, Redis coordination, and provider-specific fallback rules. None is required to deliver subscription-first paid fallback.

A smaller version works: permit mixed membership, derive `mixed`, apply fixed credential-based priorities, and use bounded priority-ordered retries. This is the recommended first release.
