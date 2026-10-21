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
- `POST /v1/responses/compact` (bounded text-summary adapter)
- Responses WebSocket requests
- `GET /v1/models`

Subpool reads each model's `supported_endpoints` metadata and forwards Responses requests to Copilot's native `/responses` endpoint when available. Models limited to `/chat/completions` use the compatibility translator for text and image messages, function and custom tools, structured output settings, streaming events, and protocol-level token usage.

Subpool reads GitHub's AI credit entitlement separately for subscription capacity and routing; token telemetry is not presented as Copilot billing.

### Limitations

On chat-only models, stateful `previous_response_id` continuation and provider-hosted tools such as web search remain unavailable. Mixed pools can fail over those requests to another compatible provider.

Image and file **tool results** require native Responses support. Chat-only
conversion rejects them with `unsupported_tool_output`; ordinary user image
messages remain supported. In-band `context_management` compaction remains
unsupported for Copilot; it is not the same contract as a final
`compaction_trigger` input item.

Copilot compaction uses bounded inference summaries through either native
Responses or the Chat translator. Standalone `/v1/responses/compact` returns a
plaintext replacement window. V2 triggers return a Subpool-owned encrypted item
that Subpool restores before subsequent inference; clients must opt into the V2
protocol. Neither mode silently truncates ordinary inference or guarantees
recovery from every 413. See the [Codex configuration](../README.md#optional-codex-v2-compaction-with-github-copilot)
and [adapter limits](provider-adapters.md) before enabling it.

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

## Google Antigravity

### Authorization

The deployment administrator must set `SUBPOOL_ANTIGRAVITY_CLIENT_ID` and `SUBPOOL_ANTIGRAVITY_CLIENT_SECRET` to a matching OAuth client pair authorized for Antigravity. Both default to empty; Subpool does not bundle Google client credentials. When both are unset, other providers remain available and the Google authorization action explains the missing configuration. Setting only one value fails startup with a configuration error. Supply credentials through the deployment environment or secret manager, never in source control. A generic Google OAuth client or Gemini API key does not establish Antigravity eligibility.

Select **Google Antigravity** in **Accounts**, enter a display name, and choose **Authorize Google**. Continue to Google, sign in, approve access, then paste the complete callback URL into Subpool. The callback is fixed to `http://localhost:8085/callback` on the administrator's computer, not the Subpool server. Subpool does not start a local listener, and no additional server port needs to be exposed. The callback page may fail to load: copy its complete URL from the browser's address bar and paste it into the authorization dialog.

The OAuth client must allow the fixed loopback callback above. `SUBPOOL_ANTIGRAVITY_REDIRECT_URI` is not read. Docker Compose passes the Client ID and Client Secret from the deployment environment with empty defaults. Changes to either value require a restart; a blank Secret does not enable secretless login for a client that requires it.

For the pre deployment workflow, set the Actions variable `SUBPOOL_ANTIGRAVITY_CLIENT_ID` and the Actions secret `SUBPOOL_ANTIGRAVITY_CLIENT_SECRET` for the deployment repository or `preproduction` environment. The workflow forwards both values into the pre container; leaving both unset keeps Google authorization unavailable without disabling other providers.

Authorization uses PKCE S256 and a random, single-use state with a ten-minute lifetime. The backend exchanges the one-time code, obtains the Google subject and email, discovers the Cloud Code project with `loadCodeAssist`, and initializes an offered default tier with `onboardUser` when necessary. The browser receives the authorization URL, not the client secret or account tokens. Tokens and project identity are encrypted at rest. Access tokens refresh before expiration using the same configured client; transient refresh failures do not imply revoked credentials. Keep the client pair stable after connecting accounts, because Google refresh tokens are bound to the client that obtained them.

### Supported APIs and quota

- `POST /v1/chat/completions`, `POST /v1/responses`, and Responses WebSocket through the existing HTTP/SSE bridge.
- `GET /v1/models` and account model discovery use `fetchAvailableModels` on the same upstream base as inference.
- Text, base64 image inputs, function tools, Responses custom/namespaced tools through the shared translator, JSON response formats, supported model-specific reasoning controls, and streaming/non-streaming output.
- Per-model remaining quota and reset times appear in Accounts. A model's 429 applies a bounded account/model cooldown rather than disabling all models on that account. Usage records input and output tokens, including generated thinking tokens; it is not a Google billing estimate.

Inference targets `https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse`, not the public Gemini API-key API. `SUBPOOL_ANTIGRAVITY_BASE_URL` and `SUBPOOL_ANTIGRAVITY_USER_AGENT` are operator overrides. There is no automatic switch to Google's daily/testing service, identity-prompt injection, model substitution, or opt-in to paid Google AI Credits overages.

### Limitations and verification

This provider uses Google's internal Cloud Code contract. Google can change eligibility, OAuth requirements, models and request fields independently of Subpool. Configuring an OAuth client does not guarantee that Google accepts a login. Use only accounts and clients you are authorized to manage, and review the service terms.

Clients must replay full conversation input. `previous_response_id`, standalone or in-band compaction, hosted tools, image/file tool outputs, remote image URLs, and generated image/audio outputs are not supported. Unsupported requests fail explicitly; Subpool does not silently drop tools, fetch arbitrary image URLs, fabricate thought signatures, or truncate conversation history. Thinking text is not promoted to normal assistant output; the Responses compatibility renderer does not expose native Gemini thinking blocks.

Tool signatures are scoped to account/model and bound to the function name and arguments. Opaque signatures and argument fingerprints are encrypted in PostgreSQL for a 24-hour lifetime, with an in-process 4,096-entry / 32 MiB cache. Ordinary Gemini function-call signatures survive process restarts and cache eviction when instances share the database and credential encryption key. Expired state or switching accounts/models requires a fresh conversation; Subpool never fabricates a replacement signature.

Claude assistant prefixes preceding function calls retain their original order, including visible text, native thinking, and standalone signature parts. Subpool's no-transcript-persistence policy forbids saving that text in the database, so these prefixes are memory-only (within the same cache bounds, with a 64 KiB per-call native-state limit). Such tool conversations require sticky instance routing and a fresh conversation after restart/eviction. Missing required native state or modified replay text fails explicitly. Opaque signature persistence does not make native Claude histories stateless or interchangeable between accounts. Prompts, assistant text, tool arguments/results, and native thinking text are never written to this state table.

Claude reasoning maps to a budget of at least 1,024 tokens. An explicit output token limit must exceed that budget; incompatible combinations fail before inference rather than silently raising the client's limit or lowering the requested effort. When reasoning is enabled without an output limit, Subpool sends a 64,000-token maximum.

Automated tests use isolated fake Google endpoints; they do not prove live account eligibility. Before production rollout, verify real Google authorization/refresh, each advertised model, tool/signature round-trips, reasoning/JSON-schema support, both wire APIs and WebSocket reconnects with an authorized test account. Request body settings do not increase Google's upstream context or payload limits.

The protocol investigation referenced [sub2api](https://github.com/Wei-Shaw/sub2api); Subpool's provider and adapter implementation is independent.

## OpenAI-compatible API

OpenAI-compatible accounts use an upstream API key and a Base URL that includes the API version path, such as `https://api.example.com/v1`. Subpool appends the appropriate `/chat/completions` or `/responses` path when forwarding requests.

These API accounts can provide paid fallback capacity in mixed pools and use the HTTP/SSE bridge for Responses traffic. Available models and capabilities depend on the configured upstream provider.
