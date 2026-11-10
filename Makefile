.PHONY: up up-db down build web tunnel migrate test

up:
	docker compose up -d --build

up-db:
	docker compose up -d db

down:
	docker compose down

build:
	go build -o bin/akhflow ./cmd/akhflow

web:
	cd web && npm install && npm run build

migrate: build
	./bin/akhflow migrate

tunnel:
	ssh -f -N -L 11434:127.0.0.1:11434 gpu-server

test:
	go test ./...
