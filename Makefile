# CIS Backend — common tasks.
# Run `make` or `make help` for the list.

.DEFAULT_GOAL := help
.PHONY: help run build test lint fmt tidy clean docker-build docker-up docker-down docker-logs

BINARY := api
BUILD_DIR := bin

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

run: ## Run the API server locally (reads .env)
	go run ./cmd/api

build: ## Compile the binary into ./bin
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY) ./cmd/api

test: ## Run the test suite
	go test ./... -count=1

lint: ## Run go vet and report unformatted files
	go vet ./...
	@gofmt -l .

fmt: ## Format all Go source
	gofmt -w .

tidy: ## Sync go.mod / go.sum
	go mod tidy

clean: ## Remove build output
	rm -rf $(BUILD_DIR)

docker-build: ## Build the Docker image
	docker build -t cis-backend:latest .

docker-up: ## Build and start the stack in the background
	docker compose up --build -d

docker-down: ## Stop and remove the stack
	docker compose down

docker-logs: ## Follow the API container logs
	docker compose logs -f api
