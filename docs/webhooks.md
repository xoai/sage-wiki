# Webhooks

Signed, at-least-once delivery of the engine's event stream to your
endpoints (SPEC-07). Configure under `serve.webhooks` — see
[configuration](guides/configuration.md#servewebhooks).

## Delivery contract

- **Body**: one event as JSON — the same shape as the JSONL audit trail
  and the SSE stream: `{"id", "time", "workspace", "type", "data": {...}}`.
- **Signature**: every delivery carries
  `X-Sage-Wiki-Signature: sha256=<hex>`, the hex HMAC-SHA256 of the RAW
  request body with your shared secret.
- **Retries**: 5xx, timeouts, and connection errors retry with exponential
  backoff (1s, 2s, 4s) up to `max_retries`. 4xx is permanent — no retry.
- **Dead letter**: events that still fail land in
  `.sage/webhooks-deadletter.jsonl` (one JSON record per event: time, url,
  event, attempts, last_error).
- **Semantics**: at-least-once. Idempotency is the consumer's job — the
  event `id` is stable across retries.
- **Filters**: `types:` limits an endpoint to specific event types
  (e.g. `[compile_finished]`); omitted = everything.

## Event types

`doc_captured`, `compile_started`, `compile_doc_finished`,
`compile_finished`, `edge_added`, `edge_invalidated`, `entity_resolved`,
`promotion_triggered`, `search_performed`, `mirror_shipped`,
`mirror_snapshot`, `usage`, `compile_skip`, `events_dropped`.

Events never contain document content, raw query text (hashed), or
filesystem paths — the workspace is carried as a name only.

## Verifying signatures

Always verify before trusting a delivery. Reject on mismatch.

**Shell (openssl):**

```sh
# $PAYLOAD_FILE: the raw request body, byte-for-byte as received
# $SECRET: the shared secret (env var or file you configured)
expected="sha256=$(openssl dgst -sha256 -hmac "$SECRET" -hex "$PAYLOAD_FILE" | awk '{print $NF}')"
[ "$expected" = "$SIGNATURE_HEADER" ] || { echo "bad signature"; exit 1; }
```

**Python:**

```python
import hashlib, hmac

def verify(secret: bytes, body: bytes, signature_header: str) -> bool:
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, signature_header)

# usage (e.g. Flask):
# body = request.get_data()           # raw bytes, do NOT re-serialize
# if not verify(secret, body, request.headers.get("X-Sage-Wiki-Signature", "")):
#     abort(401)
```

Use a constant-time comparison (`hmac.compare_digest`) — never `==`.

## Example: notify on finished compiles

```yaml
serve:
  webhooks:
    - url: https://example.com/hooks/sage-wiki
      secret_env: SAGE_WEBHOOK_SECRET
      types: [compile_finished]
```

```python
# consumer sketch
event = request.get_json()
if event["type"] == "compile_finished":
    totals = event["data"]["totals"]
    notify(f"compile {event['data']['outcome']}: "
           f"{totals['compiled']} compiled, {totals['skipped']} skipped")
```
