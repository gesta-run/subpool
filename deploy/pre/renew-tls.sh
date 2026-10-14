#!/bin/sh
set -eu

# Cloudflare redirects HTTP validation requests to this host's HTTPS webroot.
docker run --rm \
  -v /gesta/nginx/tls/subpool-letsencrypt:/etc/letsencrypt \
  -v /gesta/nginx/acme:/var/www/acme \
  certbot/certbot:v3.3.0 renew --webroot --webroot-path /var/www/acme "$@"
docker exec gesta-nginx-nginx-1 nginx -t
docker exec gesta-nginx-nginx-1 nginx -s reload
