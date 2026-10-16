# Provider adapters

Subpool separates routing, protocol adaptation, and upstream transport:

```text
Client (Responses, Chat Completions, or Responses WebSocket)
  -> Gateway (authentication, account selection, retries, persistence)
  -> Provider adapter (request conversion and response restoration)
  -> Provider client (credentials, authentication headers, HTTP transport)
  -> Upstream model
```

The client declares and executes local tools. Subpool adapts tool declarations,
calls, results, and conversation history; it does not execute those tools.

## Package responsibilities

- `internal/gateway` owns API key authorization, account selection, failover,
  credential decryption, quota state, usage persistence, and session bindings.
- `internal/adapter` defines the shared `Provider.RoundTrip` interface and
  implements the Codex, GitHub Copilot, and OpenAI-compatible adapters.
- `internal/adapter/protocol` contains request codecs, tool policies, native
  history normalization, response rendering, and streaming conversion.
- `internal/adapter/toolschema` resolves tool schema references and object unions.
- `internal/adapter/copilotstream` maintains stable native Copilot item identities.
- `internal/protocol/responseevent` parses shared response IDs, usage, and limits.
- `internal/provider` contains upstream clients and provider authentication.

Adapters have no Gateway, database, or API key dependency. The Gateway supplies
observation callbacks for usage, session IDs, and provider limits. The adapter
reports protocol observations; the Gateway decides how to persist them.

## Request and response lifecycle

Each attempt receives the original request bytes. An adapter creates its own
upstream payload without rewriting the caller's request buffer. A retry or
mixed-provider fallback therefore starts with the original client request.

The response carries its upstream format and tool mapping as private Go fields.
Mappings are scoped to one response and are never serialized into internal HTTP
headers. `Response.Render` converts the upstream response to the requested
client format. `Response.StreamHTTP` supplies a Responses stream to the WebSocket
bridge using the same conversion logic. Closing an adapted stream closes its
upstream body.

Native Codex WebSocket requests use the same Codex request normalization as HTTP,
with the transport-specific `stream` field omitted. Native Responses history,
encrypted reasoning, response IDs, and tool `call_id` values retain their
existing semantics. Custom tool results converted to function results omit the
incompatible optional result-item ID while retaining `call_id` and output.

## Capability and error handling

Copilot endpoint selection follows the model's advertised capabilities. The
adapter prefers native Responses and uses Chat Completions compatibility when
required. Existing model-specific schema and history policies stay in the codec
layer. Unsupported endpoints return `ErrUnsupported`; invalid requests retain
their specific error codes through `RequestError`. The Gateway applies the
existing retry and mixed-pool fallback rules to these results.

OpenAI-compatible adapters first try the requested wire API. They switch to the
alternate protocol only after an endpoint-level 404, 405, or 501, never after a
model-not-found, authentication, rate-limit, validation, or transport error.
Both directions reuse the shared request codecs and response renderer. Converted
Chat streams retain dense tool-call indices and requested terminal usage chunks.
Responses token limits map to standard Chat `max_completion_tokens`; the Copilot
adapter explicitly retains its existing `max_tokens` policy. Chat requests with
non-default controls that Responses cannot represent (such as multiple choices,
stop sequences, token bias, penalties, seeds, or log-probability output) return
`unsupported_parameter` before alternate inference. Null and neutral defaults
remain accepted, and native Chat requests are passed through unchanged.
Function and custom tool declarations, calls, results, and named choices are
converted as distinct types. Chat custom grammar nesting is flattened into the
Responses grammar shape.

Rich tool outputs cannot be represented in a Chat tool message. Native Responses
retains images and files; Chat conversion returns `unsupported_tool_output`
before inference instead of serializing those parts into ordinary text. Text
tool-output parts are converted to readable text. Stateful continuation and
in-band compaction likewise cannot be silently downgraded to Chat.

`POST /v1/responses/compact` uses the Responses authorization scope and forwards
to native Codex or OpenAI-compatible compaction endpoints. It preserves the full
canonical output window, including retained messages and opaque compaction
items. Compaction is non-streaming and does not create a continuation binding.
Compatible upstreams without this endpoint return `unsupported_compaction`.

Copilot subscriptions implement this explicit endpoint with bounded inference
summaries, using the model's native Responses endpoint or its Chat fallback.
The returned canonical window contains ordinary plaintext messages, not encrypted
compaction items (`X-Subpool-Compaction: text-summary`). This supports legacy
clients that install the full compact output window. It does **not** implement
Codex V2 in-band `compaction_trigger` or guarantee automatic recovery in clients
that require an opaque `compaction` item. Subpool never fabricates provider state.

Compaction preserves instruction messages, a bounded initial user request, the
latest user request, additional tool declarations, and unresolved parallel tool
call groups. Completed tool history is quoted as data, not executable tools.
The summary follows the retained latest request as an assistant message, so it
cannot be mistaken for a new user request during subsequent compactions.
Ordinary inference input is never silently truncated.
Images remain multimodal; opaque state, unsupported files, and images above the
local compaction work budget return `unsupported_compaction` instead of being
discarded. The current work budgets are 16 MiB total compact input, 128 KiB text
chunks, 2 MiB per serialized image part, 32 KiB summaries, and 128 inference calls.
These are operational safeguards, **not** advertised Copilot service limits.
The endpoint may make multiple billable inference calls and incur additional
latency. Usage from internal summaries is recorded even if a later step fails;
partial summaries are never installed. Empty-history requests require no inference.

## Compatibility and verification

The adapter refactor keeps existing public endpoints, configuration,
credentials, database schemas, tool execution, usage accounting, and session
routing compatible. Protocol fixes extend cross-endpoint conversion, preserve
tool semantics, and add explicit compaction support and capability errors.

Run `make test`, `make build`, and the race-enabled adapter/Gateway tests before
publishing. Regression coverage includes native and translated Responses,
Chat Completions, HTTP streaming, WebSocket bridging, custom and namespaced
tools, preserved history on failover, response-specific mapping isolation,
terminal usage observations, and stream cancellation.

The provider protocol matrix runs a tool-call/result/final-answer loop for both
client wire APIs, all three providers, native and Chat-only endpoint variants,
and JSON/SSE rendering. All payloads are illustrative fixtures. These local
checks do not replace live qualification of the deployed CLI versions and models.
