.PHONY: up down logs ps restart clean config migrate-up migrate-down migrate-version migrate-force

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
