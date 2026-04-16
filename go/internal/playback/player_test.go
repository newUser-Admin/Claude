package playback_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/newuser-admin/claude/interact/internal/playback"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func makeEvents(ts ...uint32) []events.WireEvent {
	evs := make([]events.WireEvent, len(ts))
	for i, t := range ts {
		evs[i] = events.WireEvent{Type: events.MsgKey, T: t, V1: float32('a' + i)}
	}
	return evs
}

func collect(evs []events.WireEvent, speed float64) ([]events.WireEvent, time.Duration) {
	var mu sync.Mutex
	var got []events.WireEvent

	p := playback.New(func(ev events.WireEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	p.Speed = speed

	start := time.Now()
	p.PlaySync(context.Background(), evs) //nolint:errcheck
	elapsed := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	return got, elapsed
}

// ─── Play (async) ─────────────────────────────────────────────────────────────

func TestPlay_DispatchesAllEvents(t *testing.T) {
	evs := makeEvents(0, 10, 20)
	var mu sync.Mutex
	var got []events.WireEvent

	p := playback.New(func(ev events.WireEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})

	ctx := context.Background()
	if err := p.Play(ctx, evs); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(evs) {
		t.Errorf("got %d events, want %d", len(got), len(evs))
	}
}

func TestPlay_EmptySlice(t *testing.T) {
	p := playback.New(func(ev events.WireEvent) {
		t.Error("action called for empty slice")
	})
	if err := p.Play(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPlay_ContextCancellation(t *testing.T) {
	// Events spaced 100ms apart; cancel immediately after start.
	evs := makeEvents(0, 100, 200, 300)
	dispatched := 0

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	p := playback.New(func(ev events.WireEvent) { dispatched++ })
	err := p.Play(ctx, evs)
	if err == nil {
		t.Error("expected context error")
	}
}

// ─── PlaySync ─────────────────────────────────────────────────────────────────

func TestPlaySync_OrderPreserved(t *testing.T) {
	evs := makeEvents(0, 5, 10)
	got, _ := collect(evs, 1.0)

	if len(got) != len(evs) {
		t.Fatalf("got %d events, want %d", len(got), len(evs))
	}
	for i, ev := range got {
		if ev.V1 != evs[i].V1 {
			t.Errorf("event[%d] V1: got %.0f, want %.0f", i, ev.V1, evs[i].V1)
		}
	}
}

func TestPlaySync_TimingRealSpeed(t *testing.T) {
	// Three events: t=0, t=50ms, t=100ms.
	evs := makeEvents(0, 50, 100)
	_, elapsed := collect(evs, 1.0)

	// Should take ~100ms; allow generous tolerance for CI.
	if elapsed < 80*time.Millisecond {
		t.Errorf("elapsed %v too fast (want ≥ 80ms)", elapsed)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("elapsed %v too slow (want ≤ 400ms)", elapsed)
	}
}

func TestPlaySync_SpeedMultiplier(t *testing.T) {
	// Events at t=0 and t=100ms; at 10x speed should finish in ~10ms.
	evs := makeEvents(0, 100)
	_, elapsed := collect(evs, 10.0)

	if elapsed > 80*time.Millisecond {
		t.Errorf("10x speed elapsed %v, want < 80ms", elapsed)
	}
}

func TestPlaySync_ZeroSpeed_DefaultsToOneX(t *testing.T) {
	evs := makeEvents(0, 50)
	_, elapsed := collect(evs, 0) // 0 should fall back to 1.0

	if elapsed < 30*time.Millisecond {
		t.Errorf("zero-speed elapsed %v too fast (want ≥ 30ms)", elapsed)
	}
}

func TestPlaySync_EmptySlice(t *testing.T) {
	p := playback.New(func(ev events.WireEvent) {
		t.Error("action called for empty slice")
	})
	if err := p.PlaySync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPlaySync_ContextCancellation(t *testing.T) {
	evs := makeEvents(0, 200, 400)
	dispatched := 0

	ctx, cancel := context.WithCancel(context.Background())

	p := playback.New(func(ev events.WireEvent) {
		dispatched++
		cancel() // cancel after first event
	})

	err := p.PlaySync(ctx, evs)
	if err == nil {
		t.Error("expected context error")
	}
	if dispatched > 1 {
		t.Errorf("dispatched %d events after cancel, want ≤ 1", dispatched)
	}
}

// ─── DefaultAction ────────────────────────────────────────────────────────────

func TestDefaultAction_Click_DecodesButton(t *testing.T) {
	// Ensure DecodeClick is exercised — just check it doesn't panic.
	evs := []events.WireEvent{
		{Type: events.MsgClick, T: 0, V1: 0.5 + 1*1000, V2: 0.5}, // right-click
		{Type: events.MsgScroll, T: 0, V1: 0, V2: -3},
		{Type: events.MsgSync, T: 99, V1: 1},
	}
	p := playback.New(nil) // uses DefaultAction
	p.PlaySync(context.Background(), evs) //nolint:errcheck
}

// ─── Nil action falls back to DefaultAction ───────────────────────────────────

func TestNew_NilActionUsesDefault(t *testing.T) {
	p := playback.New(nil)
	if p == nil {
		t.Fatal("New(nil) returned nil")
	}
	// Just ensure PlaySync doesn't panic with the default action.
	evs := []events.WireEvent{{Type: events.MsgKey, T: 0, V1: float32('x')}}
	p.PlaySync(context.Background(), evs) //nolint:errcheck
}
