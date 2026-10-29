.DEFAULT_GOAL := help
COMPOSE := docker compose
CONSOLE := $(COMPOSE) run --rm cart-service-console

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

env: ## create .env from .env.example if missing
	@test -f .env || cp .env.example .env

network: ## create the shared docker network if missing
	@docker network inspect ecom_net >/dev/null 2>&1 || docker network create ecom_net

build: env ## build images
	$(COMPOSE) --profile tools build

up: env network ## build and start, wait until healthy
	$(COMPOSE) up -d --build --wait

down: ## stop and remove the container
	$(COMPOSE) down

restart: ## restart the container
	$(COMPOSE) restart

logs: ## follow logs
	$(COMPOSE) logs -f --tail=100

ps: ## container status
	$(COMPOSE) ps

migrate: env network ## run pending migrations
	$(CONSOLE) migrate

migrate-status: env network ## show migration status
	$(CONSOLE) migrate:status

rollback: env network ## roll back the last batch (STEP=n for n batches)
	$(CONSOLE) migrate:rollback $(if $(STEP),--step=$(STEP),)

fresh: env network ## drop everything, migrate and seed (local only)
	$(CONSOLE) migrate:fresh --seed

seed: env network ## run seeders (CLASS=XSeeder for one)
	$(CONSOLE) db:seed $(if $(CLASS),--class=$(CLASS),)

shell: ## shell is not available in distroless; use `docker logs`
	@echo "distroless image: no shell. Use 'make logs' or run the console target."
