package core

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	corev1 "core.local/xo2hive/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// server implementiert den gRPC-Service Realtime und verwaltet alle Sessions.
//
// Architekturüberblick:
// - Jeder neue Client erhält beim Verbindungsaufbau eine eindeutige Session-ID.
// - Für jeden Client wird ein gepufferter sendChannel verwaltet, über den der Core Frames zum Client pusht.
// - Eingehende Frames vom Client werden ausgewertet und wahlweise:
// * als Broadcast an alle anderen gesendet (wenn Frame.To == ""), oder
// * direkt an einen Ziel-Client (wenn Frame.To != "").
// - Es gibt **keine Sicherheit** (kein TLS, keine Auth). Nur zu Demo-/Laborzwecken nutzen!

// session repräsentiert eine aktive Client-Verbindung.
type session struct {
	id     string
	ctx    context.Context
	sendCh chan *corev1.Frame
}

// Server ist der zentrale Hub.
type Server struct {
	corev1.UnimplementedRealtimeServer

	mu                 sync.RWMutex
	sessions           map[string]*session
	nextID             uint64
	sessionQueueSize   int
	sessionSendTimeout time.Duration
	maxSessions        int
}

// NewServer erzeugt einen leeren Server.
func NewServer(opts ...Option) *Server {
	cfg := defaultOptions()
	for _, opt := range opts {
		opt(&cfg)
	}

	return &Server{
		sessions:           make(map[string]*session),
		sessionQueueSize:   cfg.sessionQueueSize,
		sessionSendTimeout: cfg.sessionSendTimeout,
		maxSessions:        cfg.maxSessions,
	}
}

const (
	// DefaultSessionQueueSize ist die Anzahl Frames, die pro Session gepuffert werden.
	DefaultSessionQueueSize = 64

	// DefaultSessionSendTimeout ist die maximale Zeit, die enqueueFrame wartet,
	// bis ein Frame in den Session-Puffer geschrieben werden konnte.
	DefaultSessionSendTimeout = 2 * time.Second

	// DefaultMaxSessions begrenzt die Zahl gleichzeitiger Sessions (0 = unbegrenzt).
	DefaultMaxSessions = 0
)

type options struct {
	sessionQueueSize   int
	sessionSendTimeout time.Duration
	maxSessions        int
}

func defaultOptions() options {
	return options{
		sessionQueueSize:   DefaultSessionQueueSize,
		sessionSendTimeout: DefaultSessionSendTimeout,
		maxSessions:        DefaultMaxSessions,
	}
}

// Option passt das Verhalten des Servers an.
type Option func(*options)

// WithSessionQueueSize setzt die Puffergröße für ausgehende Frames pro Session.
func WithSessionQueueSize(size int) Option {
	return func(o *options) {
		if size > 0 {
			o.sessionQueueSize = size
		}
	}
}

// WithSessionSendTimeout definiert, wie lange enqueueFrame versucht, einen Frame zuzustellen.
func WithSessionSendTimeout(timeout time.Duration) Option {
	return func(o *options) {
		if timeout > 0 {
			o.sessionSendTimeout = timeout
		}
	}
}

// WithMaxSessions begrenzt die Anzahl gleichzeitiger Sessions (0 = unbegrenzt).
func WithMaxSessions(limit int) Option {
	return func(o *options) {
		if limit >= 0 {
			o.maxSessions = limit
		}
	}
}

// genID erzeugt eine neue aufsteigende ID als String.
func (s *Server) genID() string {
	id := atomic.AddUint64(&s.nextID, 1)
	return fmt.Sprintf("c%06d", id)
}

// broadcast sendet einen Frame an alle Sessions außer dem Absender.
func (s *Server) broadcast(fromID string, f *corev1.Frame) {
	s.mu.RLock()
	targets := make([]*session, 0, len(s.sessions))
	for id, sess := range s.sessions {
		if id != fromID {
			targets = append(targets, sess)
		}
	}
	s.mu.RUnlock()

	for _, sess := range targets {
		s.enqueueFrame(sess, f)
	}
}

// sendTo versucht, einen Frame an eine bestimmte Session-ID zuzustellen.
func (s *Server) sendTo(toID string, f *corev1.Frame) {
	s.mu.RLock()
	sess, ok := s.sessions[toID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	s.enqueueFrame(sess, f)
}

// enqueueFrame stellt sicher, dass Frames mit angemessener Backpressure in die Session gelangen.
func (s *Server) enqueueFrame(sess *session, f *corev1.Frame) {
	// Schneller Pfad: Kanal ist frei.
	select {
	case sess.sendCh <- f:
		return
	default:
	}

	timer := time.NewTimer(s.sessionSendTimeout)
	defer timer.Stop()

	select {
	case sess.sendCh <- f:
	case <-sess.ctx.Done():
	case <-timer.C:
		log.Printf("[core] Sendepuffer für %s voll, verwerfe Frame type=%s topic=%s", sess.id, f.GetType(), f.GetTopic())
	}
}

// Connect implementiert den bidirektionalen Stream. Jeder gRPC-Stream entspricht einer Session.
func (s *Server) Connect(stream corev1.Realtime_ConnectServer) error {
	if err := s.ensureCapacity(); err != nil {
		return err
	}

	// 1) Session anlegen
	id := s.genID()
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	sess := &session{
		id:     id,
		ctx:    ctx,
		sendCh: make(chan *corev1.Frame, s.sessionQueueSize),
	}

	// 2) Session im Server registrieren
	s.mu.Lock()
	if s.maxSessions > 0 && len(s.sessions) >= s.maxSessions {
		s.mu.Unlock()
		log.Printf("[core] Sessionlimit (%d) erreicht – Verbindung wird abgelehnt", s.maxSessions)
		return status.Error(codes.ResourceExhausted, "session limit reached")
	}
	s.sessions[id] = sess
	s.mu.Unlock()

	log.Printf("[core] Neue Session verbunden: %s", id)

	// 3) Zwei Routinen für Lesen/Schreiben des Streams
	doneWriter := make(chan struct{})
	go func() {
		defer close(doneWriter)
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-sess.sendCh:
				if !ok {
					return
				}
				if err := stream.Send(f); err != nil {
					log.Printf("[core] Senden an %s fehlgeschlagen: %v", id, err)
					cancel()
					return
				}
			}
		}
	}()

	// 4) Dem Client die vergebene ID mitteilen (HELLO/Welcome)
	s.enqueueFrame(sess, &corev1.Frame{
		Type:     "WELCOME",
		From:     "core",
		To:       id,
		Topic:    "core/hello",
		Payload:  []byte(fmt.Sprintf("{\"id\":\"%s\"}", id)),
		TsUnixMs: time.Now().UnixMilli(),
		Meta:     map[string]string{"role": "core"},
	})

	// Leseschleife: gRPC-Stream vom Client → Routing im Core
	for {
		in, err := stream.Recv()
		if err != nil {
			// Stream beendet oder Fehler – Session aufräumen
			break
		}

		// Robustheit: Absenderfeld ggf. setzen
		if in.From == "" {
			in.From = id
		}
		// Timestamp setzen, falls leer
		if in.TsUnixMs == 0 {
			in.TsUnixMs = time.Now().UnixMilli()
		}

		// Routing-Regeln:
		if in.To == "" {
			// Broadcast an alle außer Absender
			s.broadcast(id, in)
		} else {
			// Direktzustellung an Ziel
			s.sendTo(in.To, in)
		}
	}

	// Session entfernen
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	log.Printf("[core] Session getrennt: %s", id)
	return nil
}

// ensureCapacity prüft, ob neue Sessions zugelassen sind (0 = unbegrenzt).
func (s *Server) ensureCapacity() error {
	if s.maxSessions == 0 {
		return nil
	}

	s.mu.RLock()
	active := len(s.sessions)
	s.mu.RUnlock()

	if active >= s.maxSessions {
		log.Printf("[core] Sessionlimit (%d) erreicht – Verbindung wird abgelehnt", s.maxSessions)
		return status.Error(codes.ResourceExhausted, "session limit reached")
	}
	return nil
}

// Run startet den gRPC-Server auf dem gegebenen Port (ohne TLS, absichtlich unsicher).
func (s *Server) Run(listenAddr string) error {
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("konnte nicht auf %s lauschen: %w", listenAddr, err)
	}
	grpcServer := grpc.NewServer() // **ohne** Transport-Sicherheit, Demo only
	corev1.RegisterRealtimeServer(grpcServer, s)
	log.Printf("[core] gRPC-Server gestartet auf %s", listenAddr)
	return grpcServer.Serve(lis)
}

func (s *Server) RunWithServer(listenAddr string, grpcServer *grpc.Server) error {
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("konnte nicht auf %s lauschen: %w", listenAddr, err)
	}
	corev1.RegisterRealtimeServer(grpcServer, s)
	log.Printf("[core] gRPC-Server gestartet auf %s", listenAddr)
	return grpcServer.Serve(lis)
}
