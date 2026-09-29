.PHONY: dev web-install web-test web-build pages-test pages-build generate test build compose-up compose-dev-up compose-down

SQLC_VERSION ?= v1.30.0

dev:
	cd web && npm run dev

web-install:
	cd web && npm install

web-test:
	cd web && npm test

web-build:
	cd web && npm run build

pages-test:
	cd pages && npm test

pages-build:
	cd pages && npm run build:offline

generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

test: web-test pages-test
	go test ./...

build: web-build pages-build
	go build ./cmd/subpool

compose-up:
	docker compose up -d

compose-dev-up:
	docker compose -f compose.yaml -f compose.dev.yaml up -d --build

compose-down:
	docker compose down
