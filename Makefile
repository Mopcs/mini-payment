.PHONY: up down logs ps restart clean config migrate-up migrate-down migrate-version migrate-force \
	dashboards-validate fmt-check vet test test-race test-databases migrations-check \
	hygiene-check compose-check docker-build docker-lint prod-up prod-down ci

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f

ps:
	docker compose ps

restart:
	docker compose restart

clean:
	@echo WARNING: this removes the containers and deletes all PostgreSQL and Redis data volumes.
	docker compose down -v

config:
	docker compose config

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

migrate-version:
	go run ./cmd/migrate version

migrate-force:
	@echo WARNING: force only recovers dirty migration bookkeeping and does not execute SQL.
	go run ./cmd/migrate force $(VERSION)

dashboards-validate:
	python deployments/monitoring/validate_dashboards.py

# The targets below mirror the CI jobs, so a local run catches what CI catches.

fmt-check:
	@unformatted="$$(gofmt -l ./cmd ./internal)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt reports files that need formatting:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "gofmt: clean"

vet:
	go vet ./...

# Integration tests skip themselves unless the test databases and broker are
# reachable, which is why CI provides them. Run test-databases first to exercise
# them locally.
test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

test-databases:
	@for db in payment_db_test payment_outbox_test payment_consumer_test; do \
		echo "preparing $$db"; \
		docker compose exec -T postgres sh -c "createdb -U \$$POSTGRES_USER $$db" 2>/dev/null || true; \
		POSTGRES_DB=$$db go run ./cmd/migrate up; \
	done

migrations-check:
	./scripts/check-migrations.sh

hygiene-check:
	./scripts/check-hygiene.sh

docker-build:
	docker build --target api -t mini-payment-api:local .
	docker build --target worker -t mini-payment-worker:local .
	docker build --target consumer -t mini-payment-consumer:local .
	docker build --target migrate -t mini-payment-migrate:local .

docker-lint:
	docker run --rm -i hadolint/hadolint:2.12.0 < Dockerfile

# Renders the production stack with throwaway secrets. It fails when a required
# variable is missing, which is the point: the file must not start half
# configured.
compose-check:
	@POSTGRES_PASSWORD=ci REDIS_PASSWORD=ci RABBITMQ_PASSWORD=ci \
		JWT_SECRET=9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f \
		docker compose -f deployments/docker-compose.prod.yml config --quiet
	@echo "production compose file: valid"

prod-up:
	docker compose -f deployments/docker-compose.prod.yml \
		--env-file deployments/.env.production up -d --build

prod-down:
	docker compose -f deployments/docker-compose.prod.yml \
		--env-file deployments/.env.production down

ci: fmt-check vet migrations-check hygiene-check compose-check test-race
