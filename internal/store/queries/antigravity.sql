-- name: PutAntigravitySignature :exec
INSERT INTO antigravity_tool_signatures (provider_account_id, model, call_id, ciphertext, expires_at)
VALUES (sqlc.arg(provider_account_id), sqlc.arg(model), sqlc.arg(call_id), sqlc.arg(ciphertext), sqlc.arg(expires_at))
ON CONFLICT (provider_account_id, model, call_id) DO UPDATE
SET ciphertext = EXCLUDED.ciphertext, expires_at = EXCLUDED.expires_at;

-- name: GetAntigravitySignature :one
SELECT ciphertext FROM antigravity_tool_signatures
WHERE provider_account_id = sqlc.arg(provider_account_id)
  AND model = sqlc.arg(model) AND call_id = sqlc.arg(call_id) AND expires_at > now();

-- name: DeleteExpiredAntigravitySignatures :exec
DELETE FROM antigravity_tool_signatures WHERE expires_at <= now();
