# Subpool

Subpool is a self-hosted gateway for sharing Codex subscription accounts through employee-specific API keys. It provides an OpenAI-compatible data plane and a small administration console without storing prompts, responses, or source code.

The first release supports Codex subscriptions. Claude Code, official provider API keys, and additional provider adapters are planned for later phases.

## Run with Docker Compose

Requirements: Docker Engine with the Compose plugin.

```bash
cp .env.example .env
# Run this twice and use a different result for each key in .env.
openssl rand -base64 32
docker compose up -d --build
```

Replace every `replace-with-*` value in `.env` before starting. The console is available at `SUBPOOL_PUBLIC_URL`, or at `http://localhost:8080` with the example port mapping.

`SUBPOOL_PUBLIC_URL` is not tied to a Gesta-owned domain. Use any valid domain or internal address. Configure an origin only, without a path prefix, query, fragment, or embedded credentials. For HTTPS, terminate TLS at a reverse proxy and forward traffic to port `8080`.

If a reverse proxy supplies `X-Forwarded-For`, add only that proxy network to the comma-separated `SUBPOOL_TRUSTED_PROXY_CIDRS` setting. Leave it empty for direct access. Forwarded client addresses from untrusted peers are ignored.

The Compose stack contains exactly two services:

- `subpool`: the Go gateway and embedded React console.
- `postgres`: persistent configuration and aggregate token usage.

Codex OAuth uses OpenAI's registered loopback callback at `http://localhost:1455/auth/callback`. Keep host port `1455` available and exposed to the Subpool container.

Administrator credentials come from `SUBPOOL_ADMIN_USERNAME` and `SUBPOOL_ADMIN_PASSWORD`. Changing either value and restarting the Subpool container invalidates previous login sessions.

## Employee client setup

Create an employee API key in the console, then configure Codex CLI:

```toml
model_provider = "subpool"

[model_providers.subpool]
name = "Subpool"
base_url = "https://subpool.example.com/v1"
env_key = "SUBPOOL_API_KEY"
wire_api = "responses"
```

```bash
export SUBPOOL_API_KEY="sk-subpool-example-not-a-real-key"
```

## Development

Start the Go server on port `8080`, then run the Vite development server:

```bash
make web-install
make dev
```

Vite proxies `/api`, `/v1`, and `/healthz` to the Go server.

Run targeted checks:

```bash
make web-test
make web-build
```

## Upgrade

Back up PostgreSQL and securely back up the deployment secrets before every upgrade. The database dump is not sufficient on its own: existing upstream credentials require the exact `SUBPOOL_CREDENTIAL_KEY`, and existing employee API keys require the exact `SUBPOOL_API_KEY_HMAC_KEY`. Preserve those values in an encrypted secret manager or an access-controlled backup; do not generate replacements during restore.

```bash
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > subpool-backup.sql
docker compose build --pull subpool
docker compose up -d
```

Database migrations run when the Subpool container starts.

## Restore

Restore the saved deployment secrets to `.env` first, including the original `SUBPOOL_CREDENTIAL_KEY` and `SUBPOOL_API_KEY_HMAC_KEY`. Then restore the database into an empty PostgreSQL database after stopping the application container:

```bash
docker compose stop subpool
docker compose exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < subpool-backup.sql
docker compose start subpool
```

If either cryptographic key is missing or changed, upstream credentials or previously issued employee API keys cannot be recovered from the database backup.
