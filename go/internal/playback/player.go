// Package playback replays a slice of WireEvents with accurate relative timing,
// mirroring the replayLocal method in interaction-engine.js.
//
// Each event is dispatched via a user-supplied ActionFunc at the moment its
// original relative timestamp (scaled by Speed) has elapsed since playback
// started.
package playback

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// ActionFunc is called for each event at playback time.
// Implementations should be fast; heavy work should be deferred.
type ActionFunc func(ev events.WireEvent)

// DefaultAction is the built-in action that prints key events to stdout,
// matching the console.log output of executeAction in interaction-engine.js.
func DefaultAction(ev events.WireEvent) {
	switch ev.Type {
	case events.MsgKey:
		ch := rune(ev.V1)
		if ch >= 0x20 && ch < 0x7f {
			fmt.Fprintf(os.Stdout, "%c", ch)
		} else {
			fmt.Fprintf(os.Stdout, "[0x%02x]", int(ch))
		}
	case events.MsgClick:
		x, y, btn := events.DecodeClick(ev.V1, ev.V2)
		names := []string{"left", "right", "middle"}
		btnName := "unknown"
		if btn < len(names) {
			btnName = names[btn]
		}
		fmt.Fprintf(os.Stdout, "[click %s x=%.3f y=%.3f]\n", btnName, x, y)
	case events.MsgSync:
		fmt.Fprintf(os.Stdout, "[sync t=%d]\n", ev.T)
	case events.MsgScroll:
		fmt.Fprintf(os.Stdout, "[scroll dx=%.1f dy=%.1f]\n", ev.V1, ev.V2)
	}
}

// Player replays an interaction sequence.
type Player struct {
	// Speed is a multiplier applied to all timestamps.
	// 1.0 = real time, 2.0 = double speed, 0.5 = half speed.
	Speed float64

	action ActionFunc
}

// New returns a Player that dispatches events to action.
// Pass nil to use DefaultAction.
func New(action ActionFunc) *Player {
	if action == nil {
		action = DefaultAction
	}
	return &Player{Speed: 1.0, action: action}
}

// Play replays evs in order, respecting original timing (divided by Speed).
// It blocks until all events are dispatched or ctx is cancelled.
//
// Events are scheduled using time.AfterFunc which means wall-clock time is
// used, not CPU time — the goroutine sleeps between events.
func (p *Player) Play(ctx context.Context, evs []events.WireEvent) error {
	if len(evs) == 0 {
		return nil
	}

	speed := p.Speed
	if speed <= 0 {
		speed = 1.0
	}

	start := time.Now()

	// Use a channel of completion notifications so we can honour ctx.
	done := make(chan struct{}, 1)
	count := len(evs)
	dispatched := 0

	for _, ev := range evs {
		delay := time.Duration(float64(ev.T)/speed) * time.Millisecond
		scheduled := start.Add(delay)

		ev := ev // capture loop variable
		time.AfterFunc(time.Until(scheduled), func() {
			select {
			case <-ctx.Done():
			default:
				p.action(ev)
			}
			dispatched++
			if dispatched == count {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// PlaySync replays evs synchronously in the calling goroutine.
// It sleeps between events to match original timing.  Honour ctx cancellation.
func (p *Player) PlaySync(ctx context.Context, evs []events.WireEvent) error {
	if len(evs) == 0 {
		return nil
	}

	speed := p.Speed
	if speed <= 0 {
		speed = 1.0
	}

	start := time.Now()

	for _, ev := range evs {
		delay := time.Duration(float64(ev.T)/speed) * time.Millisecond
		target := start.Add(delay)

		wait := time.Until(target)
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			p.action(ev)
		}
	}
	return nil
}
