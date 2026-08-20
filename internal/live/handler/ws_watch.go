package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/DoIttikorn/e-commerce/internal/live"
)

const (
	// heartbeatInterval refreshes this viewer's presence. It has to be
	// comfortably shorter than the staleness window in the presence store, so a
	// slow network does not evict somebody who is still watching.
	heartbeatInterval = 25 * time.Second

	// writeTimeout bounds one message. A viewer whose connection has stalled
	// must not hold a goroutine open indefinitely waiting for a frame nobody
	// is reading.
	writeTimeout = 10 * time.Second
)

// watch upgrades to a WebSocket and streams a broadcast's events.
//
// The shape here is the standard one for a socket that has to do three things
// at once: one goroutine reads, so a client going away is noticed; the main
// loop writes; and a ticker keeps presence alive. Reading is not optional even
// though this endpoint expects no messages — a WebSocket close only surfaces
// through a read, so a handler that never reads never learns the viewer left.
func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	streamID := r.PathValue("id")

	// Confirm the stream exists before upgrading. A 404 is far easier to act on
	// than a socket that opens and immediately closes.
	if _, err := s.svc.ByID(r.Context(), streamID); err != nil {
		s.writeError(w, r, err)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The browser sends an Origin header the server is expected to check.
		// Empty means same-origin only, which is the safe default; a real
		// deployment lists the front end's origins here rather than accepting
		// every site that fancies embedding somebody else's live stream.
		OriginPatterns: nil,
	})
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "websocket upgrade failed",
			slog.String("error", err.Error()))
		return
	}
	defer func() { _ = conn.CloseNow() }()

	// A viewer is a connection, not an account: watching is public, so identity
	// here exists only to count somebody once and to remove them when they go.
	viewerID := newViewerID()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream, err := s.svc.ByID(ctx, streamID)
	if err != nil {
		closeWith(ctx, conn, err)
		return
	}

	events, viewers, err := s.svc.Join(ctx, streamID, viewerID)
	if err != nil {
		closeWith(ctx, conn, err)
		return
	}
	defer func() {
		// Detached: r.Context() is already cancelled by the time this runs, and
		// a viewer who is not removed stays in the count until they go stale.
		if err := s.svc.Leave(context.WithoutCancel(context.Background()), streamID, viewerID); err != nil {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "leaving presence failed",
				slog.String("stream_id", streamID), slog.String("error", err.Error()))
		}
	}()

	// A snapshot for this viewer alone, so they are not looking at an empty
	// screen until somebody else does something.
	//
	// Deliberately its own type rather than a second viewer.joined: Join already
	// broadcasts that, and this socket is subscribed before it joins, so sending
	// one here too would deliver the same event twice to whoever just arrived.
	if err := s.send(ctx, conn, live.Event{
		Type:              live.EventStreamState,
		StreamID:          streamID,
		Viewers:           viewers,
		FeaturedProductID: stream.FeaturedProductID,
		Status:            string(stream.Status),
		At:                time.Now().UTC(),
	}); err != nil {
		return
	}

	go s.readUntilClosed(ctx, cancel, conn)

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-events:
			if !ok {
				return
			}
			if err := s.send(ctx, conn, event); err != nil {
				return
			}

		case <-heartbeat.C:
			if err := s.presence.Heartbeat(ctx, streamID, viewerID); err != nil {
				s.log.LogAttrs(ctx, slog.LevelWarn, "heartbeat failed",
					slog.String("stream_id", streamID), slog.String("error", err.Error()))
			}
		}
	}
}

// readUntilClosed exists to notice the client going away.
//
// Anything it receives is discarded: this endpoint has nothing for a viewer to
// say. The read is how a close frame, or a dead connection, becomes a cancelled
// context and a cleaned-up viewer.
func (s *Server) readUntilClosed(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}

func (s *Server) send(ctx context.Context, conn *websocket.Conn, e live.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}

	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	return conn.Write(writeCtx, websocket.MessageText, body)
}

// closeWith reports why the socket is closing, so a client can tell "this
// stream has not started" from "the server fell over".
func closeWith(ctx context.Context, conn *websocket.Conn, err error) {
	code, reason := websocket.StatusInternalError, "internal error"

	switch {
	case errors.Is(err, live.ErrNotLive):
		code, reason = websocket.StatusPolicyViolation, "stream is not live"
	case errors.Is(err, live.ErrStreamNotFound):
		code, reason = websocket.StatusPolicyViolation, "stream not found"
	}

	_ = conn.Close(code, reason)
	_ = ctx
}

func newViewerID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
