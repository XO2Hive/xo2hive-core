# Einfache Automatisierung für Build und Container

APP_NAME := core
IMAGE := grpc-core:latest
NETWORK := xo2hive

# Pfade
PROTO_DIR := proto
PROTO_SRC := $(PROTO_DIR)/core/v1/realtime.proto
LOCAL_CONFIG := config.yaml

.PHONY: all build run docker up down logs proto tidy clean tools network

all: build

# Tools für Protobuf-Generierung installieren (nur einmal nötig)
tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Protobuf (benötigt protoc + Plugins im PATH)
proto: $(PROTO_SRC)
	protoc \
		-I $(PROTO_DIR) \
		--go_out=./ \
		--go-grpc_out=./ \
		$(PROTO_SRC)

# tidy & build hängen von generierten Protos ab (lokale Imports auflösen)
build: proto
	CGO_ENABLED=0 go build -o bin/$(APP_NAME) ./cmd/core
	chmod +x bin/$(APP_NAME)

# Lokal starten – setzt CORE_CONFIG, damit es egal ist, von wo du startest
run:
	CORE_CONFIG=$(LOCAL_CONFIG) ./bin/$(APP_NAME) || true

docker:
	docker build -t $(IMAGE) .

# Netzwerk erzeugen, falls nicht vorhanden
network:
	@if ! docker network inspect $(NETWORK) >/dev/null 2>&1; then \
	  echo "[make] Erzeuge Netzwerk $(NETWORK)"; \
	  docker network create \
	    --driver bridge \
	    --subnet 172.30.0.0/24 \
	    --gateway 172.30.0.1 \
	    $(NETWORK); \
	fi

up: network
	docker compose up -d --build

down:
	docker compose down

logs:
	docker logs -f grpc-core

tidy: proto
	go mod tidy

clean:
	rm -rf bin
