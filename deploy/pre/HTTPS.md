# Preproduction HTTPS

The console is served at `https://subpool.cloudpilot.ai`, with inference at
`https://subpool.cloudpilot.ai/v1`. Existing host ports remain available for
clients during migration.

The Gesta reverse proxy mounts its template, TLS, and ACME directories. Subpool
owns `subpool.conf.template` inside that shared template directory. Console
routes, including `/api/v1`, use port 8083; inference and WebSocket routes under
`/v1/` use port 8082 without streaming buffers.

Bootstrap a Let's Encrypt certificate on the host under
`/gesta/nginx/tls/subpool-letsencrypt` before the first HTTPS deployment. Use a
temporary DNS-only record and inbound port 80 for standalone validation, then
replace that temporary ingress rule with port 80 access from Cloudflare ranges
only, and enable Cloudflare proxying with Full (strict) TLS.
No private keys belong in the repository.

The deployment installs a daily systemd renewal timer. Cloudflare forwards ACME validation to the restricted HTTP webroot; ordinary
application requests redirect to HTTPS. Validate renewal
with `/gesta/nginx/renew-subpool-tls.sh --dry-run` after configuring DNS. Check
`journalctl -u subpool-tls-renew.service` when diagnosing renewals.

The HTTPS public URL enables Secure login cookies. Administrators should sign
in using the new domain; the existing HTTP inference endpoint is preserved.
