# Large Request Handling

## Problem

Subpool previously rejected HTTP request bodies larger than 32 MiB before routing them to a
provider. Responses API requests can embed images as base64 data URLs, and a
conversation can resend those images with later turns. The resulting JSON can
therefore exceed Subpool's limit even though the upstream API accepts it.

[OpenAI documents](https://developers.openai.com/api/docs/guides/images-vision#image-input-requirements)
a total image-input payload limit of 512 MB per request. A gateway cannot safely
adopt that value as an unconditional default because Subpool buffers and
rewrites JSON before forwarding it, which temporarily uses more memory than the
raw request size.

## Goals

- Accept image-heavy Responses API requests without requiring a client update.
- Make the HTTP request limit an operator setting instead of a code constant.
- Bound aggregate buffered request data under concurrent load.
- Make local size and capacity rejections observable without logging request
  content.
- Preserve all existing configuration when the new settings are omitted.

## Configuration

Settings provides per-request and concurrent buffer limits in MiB, persisted in
the shared database. Each HTTP request and WebSocket message snapshots the
latest limits before reading its body, including messages on existing WebSocket
connections. No restart is required, and all instances using the same database
read the saved limits.

The console submits only edited size fields. Saving employee capacity alone
preserves both saved limits and the environment fallback. Partial updates lock
the shared settings row, merge into the latest values, and validate the resulting
pair before committing, so concurrent saves cannot overwrite omitted fields.

Before an administrator saves limits, byte-valued environment variables provide
the deployment defaults:

- `SUBPOOL_MAX_REQUEST_BODY_BYTES` defaults to 268435456 (256 MiB).
- `SUBPOOL_MAX_INFLIGHT_REQUEST_BODY_BYTES` defaults to 1073741824 (1 GiB).
- `SUBPOOL_REQUEST_BODY_READ_TIMEOUT` defaults to five minutes.

Saved limits override environment defaults; the read timeout remains an
environment setting. All values must be positive. The aggregate limit must be
at least four times the per-request limit so one maximum-size Chat Completions request can always
be admitted while its decoded and rewritten forms coexist.

The per-request default is below OpenAI's documented 512 MB image-input limit.
These settings control Subpool buffering only, not upstream provider limits.
GitHub Copilot's payload limit is not assumed to match OpenAI's limit.
Operators with sufficient memory can raise them, while smaller deployments retain
a bounded default. Preproduction declares both defaults explicitly so its
effective behavior is visible in the deployment manifest.

## Request Flow

For HTTP `POST /v1/responses`, `POST /v1/chat/completions`, and inbound
Responses WebSocket messages:

1. Reject a known `Content-Length` above the per-request limit with HTTP 413
   before reading the body.
2. Reserve the estimated number of live body buffers from a process-wide byte
   budget. Responses requests reserve two copies, while WebSocket and Chat
   Completions requests reserve four to cover rewriting, failover, and network
   writes. For requests without a known length, reserve capacity incrementally
   before each chunk and reduce the estimate to the bytes actually read.
3. If capacity is unavailable, return HTTP 503 with `Retry-After: 1`, or close a
   WebSocket with status 1013, so clients can retry instead of increasing
   process memory without a bound.
4. Read at most the configured maximum plus one byte and return HTTP 413 if the
   body exceeds the limit.
5. Reduce an overestimated reservation to the actual body length and hold the
   remaining reservation until the HTTP upstream request has been issued or the
   WebSocket turn has finished.
6. Release the reservation on every success and error path.

Settings changes never replace the process-wide usage counter. Accepted requests
keep their original size and reservation limits. After lowering the aggregate
budget, new requests can be rejected temporarily while older reservations finish;
those older requests remain bounded by the budget under which they were accepted.

Large Responses payloads are rewritten one top-level field at a time. Nested
`input` data remains an opaque slice of the original JSON, avoiding a decoded
copy of base64 image data. WebSocket messages use the streaming reader so
capacity is reserved before the library buffers the complete message.

HTTP request bodies and individual WebSocket messages use the configured
timeout. The timeout does not apply to the lifetime of a WebSocket connection.
HTTP headers retain a separate short timeout.

## Observability and Privacy

Size rejections log the request path, declared or observed byte count, and the
configured limit. Capacity rejections log the requested and available limits.
Request bodies, image data, authorization headers, and prompt text are never
logged.

Copilot upstream HTTP 413 responses retain their status but use a fixed
OpenAI-compatible error with code `upstream_payload_too_large` and header
`X-Subpool-Error-Source: upstream`. Local size rejections use
`X-Subpool-Error-Source: subpool`. Upstream rejection diagnostics include only
sanitized correlation identifiers and inbound, adapted, and wire byte counts;
the upstream error body is neither read nor returned. Rewritten responses remove
stale encoding, length, digest, and cache-validator headers. Payload rejections
are not retried unchanged and do not penalize the selected account.

## Compatibility

Existing clients and deployments need no changes. The defaults apply when the
new variables are absent. Responses retain the OpenAI-compatible error envelope;
the existing HTTP 413 status remains stable, while aggregate pressure uses a
temporary HTTP 503 response or WebSocket 1013 close status.

## Verification

- Configuration tests cover defaults, overrides, malformed values, and an
  aggregate limit smaller than the per-request limit.
- Gateway tests cover exact-limit acceptance, 413 rejection, reservation
  release, concurrent capacity enforcement, WebSocket configuration, and an
  end-to-end request larger than the former 32 MiB limit.
- The full Go and web test/build suites run before a pull request is opened.
