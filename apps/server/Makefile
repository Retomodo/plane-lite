COMPOSE := docker compose -f docker-compose.dev.yml

.PHONY: up up-reference down test contract record build

up: ## Postgres, Redis and Mailpit for tests
	$(COMPOSE) up -d --wait db redis mail

up-reference: up ## + the Django reference server (needed only to record goldens)
	$(COMPOSE) --profile reference up -d --build reference

down:
	$(COMPOSE) --profile reference down

test: up ## unit + contract tests against the Go server
	go test ./... -count=1

contract: up
	go test ./contract/ -count=1 -v

record: up-reference ## re-record goldens from Django: make record RUN=TestAuth
	CONTRACT_RECORD=1 go test ./contract/ -count=1 -run '$(RUN)' -v

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/plane ./cmd/plane
