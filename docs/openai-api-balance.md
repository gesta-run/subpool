# OpenAI API credit balance

Official OpenAI-compatible accounts (`https://api.openai.com/v1`) can optionally
connect a separate API Platform browser session for balance reporting. Model API
keys do not grant access to this dashboard endpoint.

In **Accounts → Connect billing**, import the matching organization ID and browser
session key, or have a trusted local agent import them through the admin API:

- `PUT /api/v1/provider-accounts/{id}/openai-balance`
  accepts `organization_id` and `session_key`.
- `DELETE /api/v1/provider-accounts/{id}/openai-balance` disconnects billing only.

The server verifies that the inference key belongs to the selected organization
before saving encrypted credentials with a compare-and-swap update. Credential
values are never included in account responses. Browser sessions can have broader
permissions than balance reading; only administrators should connect them.

The existing provider health worker refreshes balances approximately every five
minutes. The account refresh button also reads the balance. Console and team emails
show USD remaining, the earliest expiry of an available grant, and update time.
Failures retain the last successful value and identify it as last known. Expired
or rejected authorization prompts reconnection and never disables model access.

This uses an undocumented dashboard endpoint. There is no automatic browser login
or guaranteed renewal. Connect each API organization separately. Billing display
is informational: it does not change routing, paid billing, recharge, or fallback
configuration. Organization balances include usage outside SubPool.
