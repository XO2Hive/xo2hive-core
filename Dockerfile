# Multi-Stage Build für den Core
# Ziel: kleine, statisch gelinkte Binary ohne TLS/Libs

FROM golang:1.23 AS build
WORKDIR /app

# Module zuerst, damit Caching wirkt
COPY go.mod .
RUN go mod download

# Restlicher Code
COPY . .

# Statisches Binary
ENV CGO_ENABLED=0
RUN go build -o /out/core ./cmd/core

# --- Runtime Image ---
FROM gcr.io/distroless/base-debian12
# Default-Config-Pfad innerhalb des Containers (wird auch von compose gesetzt)
ENV CORE_CONFIG=/app/config.yaml
COPY --from=build /out/core /usr/local/bin/core
COPY ./config.yaml /app/config.yaml
WORKDIR /app
EXPOSE 50051
ENTRYPOINT ["/usr/local/bin/core"]
