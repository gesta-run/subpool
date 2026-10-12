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

Image and file **tool results** require native Responses support. Chat-only
conversion rejects them with `unsupported_tool_output`; ordinary user image
messages remain supported. In-band `context_management` compaction cannot be
translated to Chat. Standalone `/v1/responses/compact` is forwarded only to
providers that implement native compaction; Copilot returns
`unsupported_compaction` instead of an endpoint-not-found error.

### GitHub Copilot CLI as a client

When connecting GitHub Copilot CLI to Subpool, use
`COPILOT_PROVIDER_TYPE=openai` and a `COPILOT_PROVIDER_BASE_URL` ending in `/v1`.
The client defaults to Chat Completions; `COPILOT_PROVIDER_WIRE_API=responses`
selects Responses. Choose the wire API and upstream model according to their
actual capabilities. Do not switch the client provider type to `anthropic` just
because the selected upstream model is Claude: Subpool does not expose an
Anthropic Messages endpoint.

### Model-specific compatibility

Subpool applies the following rules only when routing to a Copilot account and only for the exact model IDs listed:

| Model | Compatibility rule |
| --- | --- |
| `grok-4.6`, `grok-4.7` | Omit a reasoning history item's `content` only when its value is JSON `null`. Preserve encrypted reasoning, other fields, and history order. |
| `claude-sonnet-5.5`, `claude-haiku-4.5` | Serialize plain-text assistant history as strings for both native Responses and Chat routes. Preserve other roles, tool calls, rich content, and nonempty output metadata. |
| `claude-sonnet-5.5` | Reject required or named function/custom tool choices with `unsupported_tool_choice`. Automatic function calling remains supported; forced choices are not silently changed to automatic selection. |
| `gemini-3.8-flash` | Reject `xhigh` with `invalid_reasoning_effort`. Other effort values are forwarded without silent downgrade. |

In mixed pools, a known incompatible request can try another eligible provider with the original history and parameters. These model rules do not apply to Codex or OpenAI-compatible accounts, including accounts serving models with the same names.

Protocol completion does not guarantee that a model follows the latest instruction. Claude Haiku can still interpret conflict-framed formatting tests as suspicious even with correctly serialized history. These compatibility rules do not add instructions or remove earlier turns to influence model behavior.

## OpenAI-compatible API

OpenAI-compatible accounts use an upstream API key and a Base URL that includes the API version path, such as `https://api.example.com/v1`. Subpool appends the appropriate `/chat/completions` or `/responses` path when forwarding requests.

These API accounts can provide paid fallback capacity in mixed pools and use the HTTP/SSE bridge for Responses traffic. Available models and capabilities depend on the configured upstream provider.
