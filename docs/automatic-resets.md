# Automatic Codex resets

Automatic redemption is opt-in per deployment. Enable `SUBPOOL_CODEX_AUTO_RESET`
and set `SUBPOOL_AUTO_RESET_TO` to one notification email address. The existing
daily-report sender, Resend credentials, and report configuration must be enabled.
The normal daily report time and recipients are independent and remain unchanged.

All Codex subscription accounts in the deployment follow one policy, every day,
including weekends. Times are calculated in **Asia/Shanghai**:

- Aim for one hour before the earliest available reset credit expires.
- Execute during 09:00–21:00. The 21:00 trigger minute permits normal processing latency.
- For a credit expiring after 09:00 but before 10:00, use that day's 09:00.
- For a credit expiring at or before 09:00, use the previous day's 21:00.
- For a preferred trigger after 21:00, use that day's 21:00.

The service discovers accounts and credits automatically; operators do not enter
account IDs, credit IDs, weekday exceptions, or individual timers. Disabled accounts
are not redeemed. Full reported rate-limit windows cause a skipped result.

Due accounts are claimed together as a durable batch. Independent accounts can be
processed concurrently; credits for the same account remain serial. One combined
SubPool email reports each result, before/after weekly capacity, and the current
team usage snapshot. It goes only to `SUBPOOL_AUTO_RESET_TO`.

Redemption intent is stored before the upstream call. A restart never resubmits an
attempted credit. Ambiguous calls are reconciled by reading upstream status; a
missing credit alone is not proof of success. Unconfirmed results explicitly request
manual review. A frozen mail body and stable Resend key permit mail-only retries,
including outside the reset window. New redemptions never start outside the window.

Inspect `codex_reset_jobs` for scheduled execution times and outcomes, and
`codex_reset_batches` for notification acceptance. Check Resend for actual delivery;
acceptance alone does not prove delivery. The old one-shot timer must be disabled
when replacing it with this service so two schedulers cannot compete.

## Scope review

The concrete issues are expiring credits and unattended overnight failures. One
daily time calculation, existing provider integration, and existing report sender
are sufficient. No holiday calendar, account-specific rule, external workflow, or
additional mail provider is introduced. Existing manual reset endpoints, API keys,
gateway routing, and normal daily reports remain compatible. The feature defaults
to off for existing deployments.
