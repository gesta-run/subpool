.PHONY: dev web-install web-test web-build test build compose-up compose-down

dev:
	cd web && npm run dev

web-install:
	cd web && npm install

web-test:
	cd web && npm test

web-build:
	cd web && npm run build

test: web-test
	go test ./...

build: web-build
	go build ./cmd/subpool

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down
