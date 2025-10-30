package main

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"core.local/xo2hive/internal/core"

	"gopkg.in/yaml.v3"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

type Config struct {
	Listen             string        `yaml:"listen"`
	SessionQueueSize   int           `yaml:"session_queue_size"`
	SessionSendTimeout time.Duration `yaml:"session_send_timeout"`
	MaxSessions        int           `yaml:"max_sessions"`
}

// Sucht Config in dieser Reihenfolge:
// 1) CORE_CONFIG (Env)
// 2) ./config.yaml
// 3) ./cmd/core/config.yaml
// 4) /app/config.yaml (Docker)
func loadConfig() Config {
	defaultCfg := Config{
		Listen:             ":50051",
		SessionQueueSize:   core.DefaultSessionQueueSize,
		SessionSendTimeout: core.DefaultSessionSendTimeout,
		MaxSessions:        core.DefaultMaxSessions,
	}

	var candidates []string
	if p := os.Getenv("CORE_CONFIG"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		filepath.Join(".", "config.yaml"),
		filepath.Join("cmd", "core", "config.yaml"),
		filepath.Join(string(os.PathSeparator), "app", "config.yaml"),
	)

	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			c := defaultCfg
			if err := yaml.Unmarshal(b, &c); err == nil {
				if c.Listen == "" {
					c.Listen = defaultCfg.Listen
				}
				if c.SessionQueueSize <= 0 {
					c.SessionQueueSize = defaultCfg.SessionQueueSize
				}
				if c.SessionSendTimeout <= 0 {
					c.SessionSendTimeout = defaultCfg.SessionSendTimeout
				}
				if c.MaxSessions < 0 {
					c.MaxSessions = defaultCfg.MaxSessions
				}
				log.Printf("[core] Konfiguration geladen aus: %s (listen=%s)", p, c.Listen)
				return c
			}
		}
	}
	log.Printf("[core] Keine config.yaml gefunden. Fallback auf :50051")
	return defaultCfg
}

func main() {
	cfg := loadConfig()
	srv := core.NewServer(
		core.WithSessionQueueSize(cfg.SessionQueueSize),
		core.WithSessionSendTimeout(cfg.SessionSendTimeout),
		core.WithMaxSessions(cfg.MaxSessions),
	)

	// --- gRPC Keepalive ---
	kaSrv := keepalive.ServerParameters{
		Time:    15 * time.Second,
		Timeout: 5 * time.Second,
	}
	kaPolicy := keepalive.EnforcementPolicy{
		MinTime:             10 * time.Second,
		PermitWithoutStream: true,
	}

	grpcServer := grpc.NewServer(
		grpc.KeepaliveParams(kaSrv),
		grpc.KeepaliveEnforcementPolicy(kaPolicy),
	)

	// Health Service & Reflection
	hs := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, hs)
	reflection.Register(grpcServer)

	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	go func() {
		if err := srv.RunWithServer(cfg.Listen, grpcServer); err != nil {
			log.Fatalf("[core] Serverfehler: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("[core] Shutdown-Signal empfangen, stoppe gRPC-Server ...")

	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	done := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[core] gRPC-Server sauber gestoppt.")
	case <-time.After(10 * time.Second):
		log.Println("[core] GracefulStop Timeout, erzwinge Stop ...")
		grpcServer.Stop()
	}

	time.Sleep(200 * time.Millisecond)
	log.Println("[core] Core beendet.")
}
