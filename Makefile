.PHONY: up down logs ps restart clean config migrate-up migrate-down migrate-version migrate-force \
	dashboards-validate fmt-check vet test test-race test-databases migrations-check \
	hygiene-check docker-build ci

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

ci: fmt-check vet migrations-check hygiene-check test-race
