.PHONY: up down logs ps restart clean config

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
