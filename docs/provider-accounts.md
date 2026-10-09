# Provider accounts

Provider accounts supply upstream capacity to pools. Connect them from the **Accounts** page, then add one or more healthy accounts to a pool.

## Codex subscription

### Authorization

Codex subscriptions use [device-code authorization](https://developers.openai.com/codex/auth/). Copy the one-time code from Subpool, continue to OpenAI, and confirm it there. This works on remote and headless deployments without a localhost callback or an extra exposed port.

Device-code login must be enabled in ChatGPT security or workspace settings.

### Fast mode

Fast mode is controlled per Codex subscription account from the **Accounts** page. Subpool enforces the selected mode, so employees do not need to configure Fast mode in Codex.

### Image generation and editing

Codex's built-in image tool can use the same Subpool API key and bound Codex subscription. Subpool forwards JSON requests for `POST /v1/images/generations` and `POST /v1/images/edits` to the Codex subscription endpoint; no separate OpenAI API key is required.

Image endpoints use the existing `responses` API key scope.

## GitHub Copilot subscription

### Authorization

GitHub Copilot subscriptions use GitHub Device OAuth. Select **GitHub Copilot subscription**, generate a one-time code, open the displayed GitHub verification URL, and approve the device. Subpool verifies that the GitHub account has Copilot access before saving its encrypted credential.

### Supported APIs

Copilot accounts serve:

- `POST /v1/chat/completions`
- `POST /v1/responses`
- Responses WebSocket requests
- `GET /v1/models`

Subpool reads each model's `supported_endpoints` metadata and forwards Responses requests to Copilot's native `/responses` endpoint when available. Models limited to `/chat/completions` use the compatibility translator for text and image messages, function and custom tools, structured output settings, streaming events, and protocol-level token usage.

Subpool reads GitHub's AI credit entitlement separately for subscription capacity and routing; token telemetry is not presented as Copilot billing.

### Limitations

On chat-only models, stateful `previous_response_id` continuation and provider-hosted tools such as web search remain unavailable. Mixed pools can fail over those requests to another compatible provider.

## OpenAI-compatible API

OpenAI-compatible accounts use an upstream API key and a Base URL that includes the API version path, such as `https://api.example.com/v1`. Subpool appends the appropriate `/chat/completions` or `/responses` path when forwarding requests.

These API accounts can provide paid fallback capacity in mixed pools and use the HTTP/SSE bridge for Responses traffic. Available models and capabilities depend on the configured upstream provider.
