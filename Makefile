export PATH := $(HOME)/go/bin:$(HOME)/.local/bin:$(PATH)

.PHONY: all build test lint verify clean hooks docker-build docker-build-api docker-build-worker docker-up docker-down migrate-up migrate-down migrate-status migrate-create swagger codegen-sdk quality quality-lint quality-types quality-unit quality-integration quality-property quality-contract quality-mutation quality-app-security quality-supply-chain

all: verify

build:
	go build ./...

test:
	go test -v ./...

lint:
	golangci-lint run ./...

verify:
	./scripts/verify.sh

swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.8.12 init -g cmd/api/main.go -o docs/swagger

codegen-sdk: swagger
	npx --yes @openapitools/openapi-generator-cli generate -i docs/swagger/swagger.json -g typescript-fetch -o ./sdk/typescript

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

migrate-status:
	go run ./cmd/migrate status

migrate-create:
	go run ./cmd/migrate create $(name)

hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/*

docker-build: docker-build-api docker-build-worker

docker-build-api:
	docker build --target api -t go-boilerplate-api:latest .

docker-build-worker:
	docker build --target worker -t go-boilerplate-worker:latest .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

clean:
	go clean -testcache

quality: quality-lint quality-types quality-unit quality-integration quality-property quality-contract quality-mutation quality-app-security quality-supply-chain

quality-lint:
	golangci-lint run ./...

quality-types:
	go build ./...
	go vet ./...

quality-unit:
	go test -race -short ./cmd/... ./internal/...

quality-integration:
	go test -race -tags=integration ./test/integration/... ./internal/...

quality-property:
	go test -race -v ./test/property/...

quality-contract:
	go test -race -v ./test/contracts/...

quality-mutation:
	./scripts/ci/run-mutation.sh

quality-app-security:
	./scripts/ci/run-semgrep.sh && ./scripts/ci/scan-long-base64-literals.sh

quality-supply-chain:
	govulncheck ./... && ./scripts/ci/run-osv-scanner.sh && ./scripts/ci/run-trivy.sh && ./scripts/ci/run-trivy-app-secrets.sh
