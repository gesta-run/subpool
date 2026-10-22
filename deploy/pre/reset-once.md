# One-shot reset validation

This optional operator tool redeems exactly one explicitly selected credit and sends
a separate usage report to the single configured recipient through the existing
SubPool Resend account. It does not enable recurring automatic redemption or alter
the daily report schedule, recipients, API keys, or gateway routing.

Build `./cmd/subpool-reset-once` for the server's operating system and architecture.
Install the executable, `reset-once.py`, and a private `job.json` under
`/gesta/subpool/reset-once/` (root-owned, directory mode 0700, job mode 0600).
Example configuration, using illustrative values only:

```json
{
  "account_id": "example-account-id",
  "account_label": "owner@example.test",
  "credit_id": "example-credit-id",
  "run_at": "2035-01-02T21:00:00+08:00",
  "recipient": "owner@example.test",
  "origin": "http://127.0.0.1:8083"
}
```

Run `python3 /gesta/subpool/reset-once/reset-once.py --check` to validate the selected
credit, existing admin authentication, and report generation. This neither redeems
a credit nor sends mail. Install `reset-once.service` and create an operator-managed
systemd timer with **one full calendar date**, an explicit timezone, and
`Persistent=true`; do not enable the service directly. Confirm the timer's next
activation and its enabled state after installation.

The durable state is written before the reset request. A restart never submits
another reset; it only reconciles status and retries notification with a frozen
body and stable Resend idempotency key. Never remove `state.json` to retry a job.
Run under the supplied `flock` wrapper. HTTP errors and timeouts during redemption
are reported as unconfirmed unless the upstream explicitly confirms redemption.
The tool does not infer success solely from a missing credit or increased quota.

The normal report template includes all connected accounts, request shares,
employee attribution, and current quota snapshots from midnight to execution.
The event banner records reset outcome and before/after weekly quota. If the
usage database cannot be read, a fallback notification says usage is unavailable;
it never reports fabricated zero usage. Check the journal and the provider's mail
delivery status after execution; an accepted email is not proof of delivery.

## Scope review

The concrete problem is validating one cloud-side redemption while an operator
is available to recover manually. A one-shot timer is sufficient; no recurring
scheduler, new account policy, or UI control is introduced. Existing manual reset
and daily mail behavior remain compatible. Reuse of the existing admin endpoint,
SQL queries, and report renderer avoids a second redemption or reporting path.
