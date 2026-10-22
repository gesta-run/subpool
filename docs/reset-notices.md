# Weekday reset announcements

Opt in with `SUBPOOL_RESET_NOTICE_ENABLED=true` and configure the HTTPS MCP URL,
authorization header, Slack send tool name and destination channel shown in
`.env.example`. Store credentials in deployment secrets, never in the repository.

The cloud service checks during the 09:00 minute, Monday through Friday, in
Asia/Shanghai. It sends one channel announcement only when a Codex subscription
has an upcoming reset later that local day. Fresh weekly quota reset times and
pending automatic reset-credit jobs share the same summary. Past resets,
disabled accounts and stale weekly quota data are excluded. Account labels,
calendar dates, Beijing times and the type of reset come from existing records.
Announcements use Slack's explicit `<!here>` special mention to notify active
channel members rather than relying on automatic parsing of bare mention text.

The scheduler runs in SubPool and requires no employee computer. It records the
attempt in PostgreSQL before calling the configured Slack MCP tool. The date and
channel form a unique key, so restarts and concurrent workers do not repost the
announcement. Because the tool has no idempotency parameter, a failed or timed-out
attempt is recorded as unconfirmed and is not blindly retried. Inspect the
channel and service logs before any manual retry. Messages are not sent late
after the 09:00 minute or backfilled for weekends.

## Scope and compatibility review

The concrete gap is advance notice of existing resets. The minimum implementation
uses the current cloud process, quota snapshots and reset jobs, plus one small
delivery-record table. A separate workflow engine, local timer, quota forecast
or account-specific schedule is unnecessary. Defaults remain disabled, existing
data requires no backfill, and public APIs, inference routing, reset execution
and usage-email delivery are unchanged. Message delivery remains dependent on
the configured MCP service and authorization.
