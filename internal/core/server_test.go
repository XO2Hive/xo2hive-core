package core_test

import (
	"context"
	"net"
	"testing"
	"time"

	core "core.local/xo2hive/internal/core"
	corev1 "core.local/xo2hive/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

func newTestConn(t *testing.T, srv *core.Server) (*grpc.ClientConn, func()) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	if srv == nil {
		srv = core.NewServer()
	}
	corev1.RegisterRealtimeServer(grpcServer, srv)

	go func() {
		if err := grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			t.Logf("grpc server stopped: %v", err)
		}
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
	}

	return conn, cleanup
}

func connectClient(t *testing.T, client corev1.RealtimeClient) (*clientStream, *corev1.Frame) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stream, err := client.Connect(ctx)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}

	welcome, err := stream.Recv()
	if err != nil {
		cancel()
		t.Fatalf("welcome: %v", err)
	}

	return &clientStream{Realtime_ConnectClient: stream, cancel: cancel}, welcome
}

type clientStream struct {
	corev1.Realtime_ConnectClient
	cancel context.CancelFunc
}

func (s *clientStream) Close() {
	_ = s.CloseSend()
	s.cancel()
}

func mustRecv(t *testing.T, stream *clientStream, timeout time.Duration) *corev1.Frame {
	t.Helper()

	type result struct {
		frame *corev1.Frame
		err   error
	}

	ch := make(chan result, 1)
	go func() {
		frame, err := stream.Recv()
		ch <- result{frame: frame, err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("recv failed: %v", res.err)
		}
		return res.frame
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for frame")
		return nil
	}
}

func TestConnectSendsWelcome(t *testing.T) {
	conn, cleanup := newTestConn(t, nil)
	defer cleanup()

	client := corev1.NewRealtimeClient(conn)
	stream, welcome := connectClient(t, client)
	defer stream.Close()

	if got := welcome.GetType(); got != "WELCOME" {
		t.Fatalf("welcome type = %s, want WELCOME", got)
	}
	if welcome.GetTo() == "" {
		t.Fatalf("welcome missing assigned session id")
	}
	if welcome.GetFrom() != "core" {
		t.Fatalf("welcome from = %s, want core", welcome.GetFrom())
	}
}

func TestBroadcastDeliversToPeers(t *testing.T) {
	conn, cleanup := newTestConn(t, nil)
	defer cleanup()

	client := corev1.NewRealtimeClient(conn)
	streamA, welcomeA := connectClient(t, client)
	defer streamA.Close()
	streamB, welcomeB := connectClient(t, client)
	defer streamB.Close()

	idA := welcomeA.GetTo()
	idB := welcomeB.GetTo()

	if idA == idB {
		t.Fatalf("expected unique session ids, both got %s", idA)
	}

	payload := []byte("broadcast payload")
	if err := streamA.Send(&corev1.Frame{
		Type:    "EVENT",
		Topic:   "test/broadcast",
		Payload: payload,
	}); err != nil {
		t.Fatalf("send broadcast: %v", err)
	}

	msg := mustRecv(t, streamB, time.Second)
	if msg.GetTopic() != "test/broadcast" {
		t.Fatalf("broadcast topic = %s, want test/broadcast", msg.GetTopic())
	}
	if msg.GetFrom() != idA {
		t.Fatalf("broadcast from = %s, want %s", msg.GetFrom(), idA)
	}
	if string(msg.GetPayload()) != string(payload) {
		t.Fatalf("broadcast payload mismatch")
	}
	if msg.GetTo() != "" {
		t.Fatalf("broadcast to = %s, want empty (broadcast)", msg.GetTo())
	}
}

func TestDirectDelivery(t *testing.T) {
	conn, cleanup := newTestConn(t, nil)
	defer cleanup()

	client := corev1.NewRealtimeClient(conn)
	streamA, welcomeA := connectClient(t, client)
	defer streamA.Close()
	streamB, welcomeB := connectClient(t, client)
	defer streamB.Close()

	idA := welcomeA.GetTo()
	idB := welcomeB.GetTo()

	payload := []byte("direct payload")
	if err := streamA.Send(&corev1.Frame{
		Type:    "EVENT",
		Topic:   "test/direct",
		To:      idB,
		Payload: payload,
	}); err != nil {
		t.Fatalf("send direct: %v", err)
	}

	msg := mustRecv(t, streamB, time.Second)
	if msg.GetTopic() != "test/direct" {
		t.Fatalf("direct topic = %s, want test/direct", msg.GetTopic())
	}
	if msg.GetFrom() != idA {
		t.Fatalf("direct from = %s, want %s", msg.GetFrom(), idA)
	}
	if msg.GetTo() != idB {
		t.Fatalf("direct to = %s, want %s", msg.GetTo(), idB)
	}
	if string(msg.GetPayload()) != string(payload) {
		t.Fatalf("direct payload mismatch")
	}
}

func TestCustomSessionOptions(t *testing.T) {
	srv := core.NewServer(
		core.WithSessionQueueSize(1),
		core.WithSessionSendTimeout(50*time.Millisecond),
	)

	conn, cleanup := newTestConn(t, srv)
	defer cleanup()

	client := corev1.NewRealtimeClient(conn)
	stream, welcome := connectClient(t, client)
	defer stream.Close()

	if welcome.GetTo() == "" {
		t.Fatalf("custom options welcome missing session id")
	}
}

func TestMaxSessionsLimit(t *testing.T) {
	srv := core.NewServer(
		core.WithMaxSessions(1),
	)

	conn, cleanup := newTestConn(t, srv)
	defer cleanup()

	client := corev1.NewRealtimeClient(conn)
	first, welcome := connectClient(t, client)
	defer first.Close()

	if welcome.GetTo() == "" {
		t.Fatalf("first session missing id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	second, err := client.Connect(ctx)
	if err != nil {
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got %v", err)
		}
		if st.Code() != codes.ResourceExhausted {
			t.Fatalf("expected ResourceExhausted, got %v", st.Code())
		}
		return
	}

	defer second.CloseSend()

	_, recvErr := second.Recv()
	if recvErr == nil {
		t.Fatalf("expected error on second session due to limit")
	}

	st, ok := status.FromError(recvErr)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", recvErr)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", st.Code())
	}
}
