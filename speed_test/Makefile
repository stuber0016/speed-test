# Pick up CLIENT_PORT etc. from .env so messages show the real URL.
-include .env

.PHONY: up down speedtest list get keys test logs

## up: build and start target + client, wait until both are healthy
up:
	docker compose up --build --detach --wait
	@echo "Client UI: http://localhost:$(or $(CLIENT_PORT),8080)  (API key: CLIENT_API_KEY in .env)"
	@echo "Run a test from the terminal with: make speedtest"

## down: stop and remove the containers
down:
	docker compose down

# stctl ships inside the client image and already has CLIENT_API_KEY in its
# environment, so the CLI targets run it in the running client container.

## speedtest: run a speed test from the CLI and watch it live
speedtest:
	docker compose exec client /stctl run $(ARGS)

## list: show recent tests
list:
	docker compose exec client /stctl list

## get: show one test, e.g. make get ID=3f2a9c...
get:
	docker compose exec client /stctl get $(ID)

## keys: replace the tokens in .env with random ones (then `make up`)
keys:
	./scripts/gen-keys.sh

## test: run the Go unit and integration tests
test:
	go test -race ./...

## logs: follow service logs
logs:
	docker compose logs --follow
