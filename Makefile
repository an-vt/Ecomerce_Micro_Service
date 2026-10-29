.DEFAULT_GOAL := help
SHELL := /bin/bash

# Every module listed in go.work (platform-go, proto, services, e2e).
MODULES := $(shell awk '/^[[:space:]]+\.\//{print $$1}' go.work)

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

fmt: ## gofmt + goimports on all modules
	@for m in $(MODULES); do (cd $$m && gofmt -w -s . ); done

vet: ## go vet on all modules
	@for m in $(MODULES); do echo "== vet $$m"; (cd $$m && go vet ./... ) || exit 1; done

lint: vet ## golangci-lint (incl. depguard DDD layer rules) on all modules
	@for m in $(MODULES); do echo "== lint $$m"; (cd $$m && golangci-lint run ./... ) || exit 1; done

tidy: ## go mod tidy on all modules
	@for m in $(MODULES); do (cd $$m && go mod tidy ); done
	go work sync

tidy-check: tidy ## fail if go.mod/go.sum are not tidy
	@git diff --exit-code -- '*.mod' '*.sum' || (echo "run 'make tidy'"; exit 1)

test: ## unit tests with -race (no Docker)
	@for m in $(MODULES); do echo "== test $$m"; (cd $$m && go test -race -count=1 ./... ) || exit 1; done

test-integration: ## unit + integration tests (testcontainers, needs Docker)
	@for m in $(MODULES); do echo "== integration $$m"; (cd $$m && go test -race -count=1 -p 1 -tags=integration ./... ) || exit 1; done

test-e2e: ## e2e tests against the running stack (make start first)
	cd e2e && E2E_REQUIRED=1 go test -count=1 -tags=e2e -p 1 ./...

cover: ## unit + integration coverage (needs Docker), fails under 80% — what CI runs
	COVER_INTEGRATION=1 ./scripts/coverage.sh

cover-unit: ## unit-test coverage only (no Docker; database code is covered by integration tests)
	./scripts/coverage.sh

gen-proto: ## regenerate gRPC code with buf into proto/gen/go
	cd proto && buf generate

gen-keys: ## generate local RS256 key pair into secrets/
	./scripts/gen-keys.sh

check-infra: ## validate compose files and prometheus rules
	./scripts/check-infra.sh

start: ## start shared infra + all implemented services
	./scripts/start-all.sh

stop: ## stop everything (VOLUMES=1 also wipes data)
	./scripts/stop-all.sh $(if $(VOLUMES),--volumes,)

status: ## health table
	./scripts/status.sh

migrate: ## run migrations of every running service
	./scripts/migrate-all.sh

seed: ## run seeders of every running service
	./scripts/seed-all.sh

new-service: ## scaffold a service: NAME= SHORT= HTTP= GRPC= DB=pg|mongo|none
	./tools/newservice/new-service.sh NAME=$(NAME) SHORT=$(SHORT) HTTP=$(HTTP) GRPC=$(GRPC) DB=$(DB)

.PHONY: help fmt vet lint tidy tidy-check test test-integration test-e2e cover cover-unit gen-proto gen-keys check-infra start stop status migrate seed new-service
