# Go gRPC Core (MIT)


Ein minimaler gRPC-Core-Hub in Go. Alle Clients verbinden sich per bidi-Stream und können Nachrichten
über den Core broadcasten oder direkt an andere Clients adressieren. **Keine Sicherheit (kein TLS)**.


## Features
- Bidirektionaler gRPC-Stream (Service: `Realtime.Connect`)
- Broadcast (leerers `to`) und Direktzustellung (`to=clientID`)
- Automatische Vergabe von Client-IDs (`c000001`, ...)
- Sehr einfache In-Memory-Verwaltung


## Schnellstart
```bash
# 1) Abhängigkeiten synchronisieren
make tidy


# 2) (Optional) Protos generieren, falls geändert
make proto


# 3) Lokal bauen
make build


# 4) Container bauen & starten (mit fixem Netz und fester Core-IP 172.25.0.2)
make up


# Logs ansehen
make logs


# Stoppen
make down
```

## Konfiguration
Der Core liest standardmäßig `config.yaml` (oder den in `CORE_CONFIG` gesetzten Pfad). Neben `listen` sind folgende Parameter verfügbar:

- `session_queue_size`: Anzahl gepufferter Frames pro Session (Default 64).
- `session_send_timeout`: Dauer, wie lange das Backend beim Schreiben wartet, z. B. `2s`.
- `max_sessions`: Maximale Anzahl paralleler Sessions (0 = unbegrenzt).

### Beispielprofile
- `config.dev.yaml`: Lokale Entwicklung mit kleinen Puffern und unbegrenzten Sessions.
- `config.prod.yaml`: Beispielwerte für Produktion (größerer Puffer, Limit auf 500 Sessions).

## Compose/Deployment
Der `docker-compose.yml`-Stack bindet standardmäßig `./config.yaml` nach `/app/config.yaml` und verwendet damit das Entwicklungsprofil ohne Session-Limit. Für produktionsnahe Tests oder Deployments sollte ein Profil mit Limits aktiviert werden.

- Option 1: Die Datei `config.prod.yaml` nach `config.yaml` kopieren, bevor `make up` bzw. `docker compose up` ausgeführt wird.
- Option 2: In `docker-compose.yml` den Bind-Mount auf `config.prod.yaml` umstellen:

```yaml
    volumes:
      - ./config.prod.yaml:/app/config.yaml:ro
```

- Option 3: Eine eigene Konfiguration verwenden und über `CORE_CONFIG` einbinden:

```bash
CORE_CONFIG=/app/custom-config.yaml docker compose up -d
```

Achte darauf, dass produktive Setups ein sinnvolles `max_sessions` setzen, damit der Core nicht unbegrenzt Sessions akzeptiert.
