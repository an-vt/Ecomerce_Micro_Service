.DEFAULT_GOAL := help
COMPOSE := docker compose
# OBSERVABILITY=0 skips Prometheus/Grafana/Loki/Tempo/Alloy/exporters
PROFILE := $(if $(filter 0,$(OBSERVABILITY)),,--profile observability)

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

env: ## create .env from .env.example if missing
	@test -f .env || cp .env.example .env

network: ## create the shared docker network if missing
	@docker network inspect ecom_net >/dev/null 2>&1 || docker network create ecom_net

up: env network ## start stores, broker, mail (+ observability unless OBSERVABILITY=0)
	$(COMPOSE) $(PROFILE) up -d --wait --wait-timeout 180
	$(COMPOSE) wait ecom-kafka-init >/dev/null 2>&1 || true

down: ## stop everything (keeps data)
	$(COMPOSE) --profile observability down

reset: ## stop everything and delete all data volumes
	$(COMPOSE) --profile observability down -v

logs: ## follow logs (SVC=ecom-kafka for one)
	$(COMPOSE) --profile observability logs -f --tail=100 $(SVC)

ps: ## container status
	$(COMPOSE) --profile observability ps

psql: ## psql as the superuser (DB=order_db for one database)
	docker exec -it ecom-postgres psql -U postgres $(DB)

mongosh: ## mongosh as root
	docker exec -it ecom-mongo mongosh -u root -p root --authenticationDatabase admin

topics: ## list kafka topics
	docker exec ecom-kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list
