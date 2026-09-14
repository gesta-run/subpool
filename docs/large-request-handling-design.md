# Large Request Handling

## Problem

Subpool rejects HTTP request bodies larger than 32 MiB before routing them to a
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

Two byte-valued environment variables control HTTP request buffering:

- `SUBPOOL_MAX_REQUEST_BODY_BYTES` defaults to 268435456 (256 MiB).
- `SUBPOOL_MAX_INFLIGHT_REQUEST_BODY_BYTES` defaults to 1073741824 (1 GiB).
- `SUBPOOL_REQUEST_BODY_READ_TIMEOUT` defaults to five minutes.

All values must be positive. The aggregate limit must be at least four times
the per-request limit so one maximum-size Chat Completions request can always
be admitted while its decoded and rewritten forms coexist.

The per-request default is intentionally below the upstream 512 MB limit.
Operators with sufficient memory can raise it, while smaller deployments retain
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
   writes. For requests without a known length, reserve the per-request maximum
   until the actual length is known.
3. If capacity is unavailable, return HTTP 503 with `Retry-After: 1`, or close a
   WebSocket with status 1013, so clients can retry instead of increasing
   process memory without a bound.
4. Read at most the configured maximum plus one byte and return HTTP 413 if the
   body exceeds the limit.
5. Reduce an overestimated reservation to the actual body length and hold the
   remaining reservation until the HTTP upstream request has been issued or the
   WebSocket turn has finished.
6. Release the reservation on every success and error path.

Large Responses payloads are rewritten one top-level field at a time. Nested
`input` data remains an opaque slice of the original JSON, avoiding a decoded
copy of base64 image data. WebSocket messages use the streaming reader so
capacity is reserved before the library buffers the complete message.

HTTP and WebSocket body reads use the configured timeout. HTTP headers retain a
separate short timeout.

## Observability and Privacy

Size rejections log the request path, declared or observed byte count, and the
configured limit. Capacity rejections log the requested and available limits.
Request bodies, image data, authorization headers, and prompt text are never
logged.

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
