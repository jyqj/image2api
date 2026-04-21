.PHONY: help run build tidy test fmt vet db-init docker-up docker-down docker-logs

SHELL := /bin/sh
APP_NAME := gpt2api
BIN_DIR := bin

MYSQL_HOST ?= 127.0.0.1
MYSQL_PORT ?= 3306
MYSQL_USER ?= gpt2api
MYSQL_PASSWORD ?= gpt2api
MYSQL_DATABASE ?= gpt2api

help:
	@echo "Targets:"
	@echo "  run              - go run cmd/server"
	@echo "  build            - build binary to bin/$(APP_NAME)"
	@echo "  tidy             - go mod tidy"
	@echo "  test             - go test ./..."
	@echo "  fmt              - gofmt -w"
	@echo "  vet              - go vet ./..."
	@echo "  db-init          - initialize empty MySQL database from sql/database.sql"
	@echo "  docker-up        - docker compose up -d"
	@echo "  docker-down      - docker compose down"
	@echo "  docker-logs      - docker compose logs -f"

run:
	go run ./cmd/server

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "-s -w" -o $(BIN_DIR)/$(APP_NAME) ./cmd/server

tidy:
	go mod tidy

test:
	go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

db-init:
	MYSQL_PWD="$(MYSQL_PASSWORD)" mysql -h "$(MYSQL_HOST)" -P "$(MYSQL_PORT)" -u "$(MYSQL_USER)" "$(MYSQL_DATABASE)" < sql/database.sql

docker-up:
	docker compose -f deploy/docker-compose.yml up -d

docker-down:
	docker compose -f deploy/docker-compose.yml down

docker-logs:
	docker compose -f deploy/docker-compose.yml logs -f
