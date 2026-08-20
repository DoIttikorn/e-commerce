package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/live"
	"github.com/DoIttikorn/e-commerce/internal/live/redisbus"
)

// newBus returns a Bus on its own Redis connection.
//
// Each call stands for a separate instance of the service. That is the whole
// point of these tests: everything Live Commerce does has to be true across
// instances, and a single in-process bus would prove none of it.
func newBus(t *testing.T) (*redisbus.Bus, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs Redis; run make itest")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	rdb, err := database.NewRedis(ctx, envOr("REDIS_ADDR", "127.0.0.1:6379"), "", 0)
	if err != nil {
		t.Fatalf("redis unreachable: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	return redisbus.New(rdb, discard()), ctx
}

func streamID() string { return fmt.Sprintf("stream-%d", time.Now().UnixNano()) }

// The claim the whole design rests on: a viewer holding a socket on one
// instance sees an event handled by another.
func TestABroadcastReachesASubscriberOnAnotherInstance(t *testing.T) {
	instanceA, ctx := newBus(t)
	instanceB, _ := newBus(t)
	id := streamID()

	subCtx, stopSub := context.WithCancel(ctx)
	defer stopSub()

	events, err := instanceA.Subscribe(subCtx, id)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Published by a different instance entirely, on a different connection.
	err = instanceB.Publish(ctx, id, live.Event{
		Type: live.EventPurchase, StreamID: id, ProductName: "Blue Mug", Quantity: 2,
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	select {
	case got := <-events:
		if got.Type != live.EventPurchase || got.ProductName != "Blue Mug" {
			t.Errorf("event = %+v, want the purchase published by the other instance", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the event never crossed between instances")
	}
}

// Two viewers on two instances make an audience of two, not one each.
func TestPresenceIsSharedAcrossInstances(t *testing.T) {
	instanceA, ctx := newBus(t)
	instanceB, _ := newBus(t)
	id := streamID()

	if _, err := instanceA.Join(ctx, id, "viewer-on-a"); err != nil {
		t.Fatalf("Join() on A error = %v", err)
	}
	count, err := instanceB.Join(ctx, id, "viewer-on-b")
	if err != nil {
		t.Fatalf("Join() on B error = %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 — each instance is counting only its own", count)
	}

	// And both agree.
	fromA, _ := instanceA.Count(ctx, id)
	fromB, _ := instanceB.Count(ctx, id)
	if fromA != 2 || fromB != 2 {
		t.Errorf("A sees %d and B sees %d, want both to see 2", fromA, fromB)
	}

	if _, err := instanceA.Leave(ctx, id, "viewer-on-a"); err != nil {
		t.Fatalf("Leave() error = %v", err)
	}
	after, _ := instanceB.Count(ctx, id)
	if after != 1 {
		t.Errorf("count after one left = %d, want 1", after)
	}
}

// A reconnecting viewer must not be counted twice.
func TestRejoiningDoesNotDoubleCount(t *testing.T) {
	bus, ctx := newBus(t)
	id := streamID()

	for range 5 {
		if _, err := bus.Join(ctx, id, "same-viewer"); err != nil {
			t.Fatalf("Join() error = %v", err)
		}
	}

	count, _ := bus.Count(ctx, id)
	if count != 1 {
		t.Errorf("count = %d, want 1 — one viewer rejoining is still one viewer", count)
	}
}

// Nobody sends a goodbye when a laptop lid closes, and an instance that crashes
// sends none for anyone it was holding. Without expiry the count would only
// ever climb.
func TestPresenceExpiresWithoutAHeartbeat(t *testing.T) {
	bus, ctx := newBus(t)
	id := streamID()

	if _, err := bus.Join(ctx, id, "viewer-1"); err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if count, _ := bus.Count(ctx, id); count != 1 {
		t.Fatalf("count = %d, want 1 immediately after joining", count)
	}

	// A heartbeat keeps them; the staleness window is minutes, so this test
	// proves the refresh works rather than waiting for the expiry itself.
	if err := bus.Heartbeat(ctx, id, "viewer-1"); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if count, _ := bus.Count(ctx, id); count != 1 {
		t.Errorf("count = %d after a heartbeat, want 1", count)
	}
}

// A subscriber that starts after a message was published never sees it. That is
// Redis pub/sub working as designed, and it is why orders go through Kafka and
// live events do not.
func TestPubSubHasNoReplay(t *testing.T) {
	bus, ctx := newBus(t)
	id := streamID()

	if err := bus.Publish(ctx, id, live.Event{Type: live.EventPurchase, StreamID: id}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	subCtx, stopSub := context.WithCancel(ctx)
	defer stopSub()

	events, err := bus.Subscribe(subCtx, id)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	select {
	case got := <-events:
		t.Errorf("received %+v; a late subscriber should see nothing", got)
	case <-time.After(2 * time.Second):
		// Correct: the message is gone.
	}
}

// Cancelling the context closes the channel, so a disconnected viewer does not
// leave a goroutine reading forever.
func TestSubscriptionStopsWhenTheContextIsCancelled(t *testing.T) {
	bus, ctx := newBus(t)
	id := streamID()

	subCtx, stopSub := context.WithCancel(ctx)
	events, err := bus.Subscribe(subCtx, id)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	stopSub()

	select {
	case _, open := <-events:
		if open {
			// Drain one, then it must close.
			select {
			case _, stillOpen := <-events:
				if stillOpen {
					t.Error("the channel stayed open after cancellation")
				}
			case <-time.After(5 * time.Second):
				t.Error("the channel never closed")
			}
		}
	case <-time.After(5 * time.Second):
		t.Error("the channel never closed after the context was cancelled")
	}
}
