package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"google.golang.org/grpc"
)

func TestPublishBinaryUpdateBroadcastsAndSticks(t *testing.T) {
	srv := &Server{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		subscribers: make(map[chan *clydev1.SubscribeRegistryResponse]registrySubscriberState),
	}
	ch := make(chan *clydev1.SubscribeRegistryResponse, 1)
	srv.subscribers[ch] = true

	srv.publishBinaryUpdate("/tmp/clyde", "mtime_changed", "abc123")

	ev := <-ch
	if ev.GetKind() != clydev1.SubscribeRegistryResponse_KIND_CLYDE_BINARY_UPDATED {
		t.Fatalf("kind=%v want CLYDE_BINARY_UPDATED", ev.GetKind())
	}
	if ev.GetBinaryPath() != "/tmp/clyde" {
		t.Fatalf("binary path=%q want /tmp/clyde", ev.GetBinaryPath())
	}
	if ev.GetBinaryReason() != "mtime_changed" {
		t.Fatalf("binary reason=%q want mtime_changed", ev.GetBinaryReason())
	}
	if ev.GetBinaryHash() != "abc123" {
		t.Fatalf("binary hash=%q want abc123", ev.GetBinaryHash())
	}
	if srv.binaryUpdate == nil || srv.binaryUpdate.GetBinaryPath() != "/tmp/clyde" {
		t.Fatalf("binary update was not retained for later subscribers")
	}
}

func TestSubscribeRegistrySendsStickyBinaryUpdate(t *testing.T) {
	srv := &Server{
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		subscribers:  make(map[chan *clydev1.SubscribeRegistryResponse]registrySubscriberState),
		binaryUpdate: &clydev1.SubscribeRegistryResponse{Kind: clydev1.SubscribeRegistryResponse_KIND_CLYDE_BINARY_UPDATED, BinaryPath: "/tmp/clyde"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeRegistryStream{
		ctx:  ctx,
		sent: make(chan *clydev1.SubscribeRegistryResponse, 1),
	}
	done := make(chan error, 1)
	go func() {
		done <- srv.SubscribeRegistry(&clydev1.SubscribeRegistryRequest{}, stream)
	}()

	select {
	case ev := <-stream.sent:
		if ev.GetKind() != clydev1.SubscribeRegistryResponse_KIND_CLYDE_BINARY_UPDATED {
			t.Fatalf("kind=%v want CLYDE_BINARY_UPDATED", ev.GetKind())
		}
		if ev.GetBinaryPath() != "/tmp/clyde" {
			t.Fatalf("binary path=%q want /tmp/clyde", ev.GetBinaryPath())
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for sticky binary update")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SubscribeRegistry returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for SubscribeRegistry to stop")
	}
}

type fakeRegistryStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan *clydev1.SubscribeRegistryResponse
}

func (s *fakeRegistryStream) Context() context.Context {
	return s.ctx
}

func (s *fakeRegistryStream) Send(ev *clydev1.SubscribeRegistryResponse) error {
	s.sent <- ev
	return nil
}
