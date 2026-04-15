// Package record captures keyboard (and optionally mouse) interactions and
// emits WireEvents with accurate relative timestamps.
//
// The Recorder puts stdin into raw mode so individual key presses are received
// immediately. This is cross-platform (Linux, macOS, Windows) and requires no
// CGo or system-level privileges.
//
// For system-wide capture beyond the current terminal (e.g. capturing key
// events in other windows) a platform-specific backend must be plugged in via
// SetBackend. The default terminal backend is sufficient for recording shell
// sessions and TUI application interactions.
package record

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// Backend is an optional interface for system-wide event capture.
// Implement this on each platform and register it with SetBackend to record
// events outside the current terminal.
type Backend interface {
	// Start begins capture and streams events to ch until ctx is cancelled.
	Start(ctx context.Context, ch chan<- events.WireEvent) error
}

var globalBackend Backend

// SetBackend registers a platform-specific capture backend.
// When nil (the default) the terminal-mode capturer is used.
func SetBackend(b Backend) { globalBackend = b }

// ─────────────────────────────────────────────────────────────────────────────

// Recorder captures input events, timestamps them relative to the recording
// start, and optionally fans them out to registered listeners.
type Recorder struct {
	listeners []chan events.WireEvent
	startTime time.Time
	events    []events.WireEvent
}

// New returns a ready-to-use Recorder.
func New() *Recorder { return &Recorder{} }

// Subscribe registers a channel that receives every captured event in real
// time. The caller must drain the channel to avoid blocking the recorder.
func (r *Recorder) Subscribe() <-chan events.WireEvent {
	ch := make(chan events.WireEvent, 256)
	r.listeners = append(r.listeners, ch)
	return ch
}

// Events returns a copy of all events captured so far.
func (r *Recorder) Events() []events.WireEvent {
	cp := make([]events.WireEvent, len(r.events))
	copy(cp, r.events)
	return cp
}

// Record runs until ctx is cancelled, capturing events from stdin (or the
// registered backend). Press Ctrl-C / cancel the context to stop.
//
// All captured events are stored in memory and also fanned out to any
// subscribed channels.
func (r *Recorder) Record(ctx context.Context) error {
	r.events = nil
	r.startTime = time.Now()

	if globalBackend != nil {
		ch := make(chan events.WireEvent, 256)
		go func() {
			for ev := range ch {
				r.emit(ev)
			}
		}()
		return globalBackend.Start(ctx, ch)
	}

	return r.recordTerminal(ctx)
}

// recordTerminal captures key events from the current terminal's stdin by
// switching it to raw mode.
func (r *Recorder) recordTerminal(ctx context.Context) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal; attach a terminal or use a backend for system-wide capture")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	fmt.Fprintln(os.Stderr, "[record] recording — press Ctrl-C to stop")

	buf := make([]byte, 8)
	done := ctx.Done()

	for {
		select {
		case <-done:
			return nil
		default:
		}

		// Non-blocking read attempt via a goroutine so we can honour ctx.
		type result struct {
			n   int
			err error
		}
		res := make(chan result, 1)
		go func() {
			n, err := os.Stdin.Read(buf)
			res <- result{n, err}
		}()

		select {
		case <-done:
			return nil
		case rr := <-res:
			if rr.err != nil {
				return rr.err
			}
			if rr.n == 0 {
				continue
			}
			for i := 0; i < rr.n; i++ {
				b := buf[i]
				// Ctrl-C (0x03) stops recording
				if b == 0x03 {
					return nil
				}
				ts := r.ts()
				ev := events.WireEvent{
					Type: events.MsgKey,
					T:    ts,
					V1:   float32(b),
					V2:   0,
				}
				r.emit(ev)
			}
		}
	}
}

// ts returns the elapsed milliseconds since recording started.
func (r *Recorder) ts() uint32 {
	return uint32(time.Since(r.startTime).Milliseconds())
}

// emit stores an event and fans it out to all listeners.
func (r *Recorder) emit(ev events.WireEvent) {
	r.events = append(r.events, ev)
	for _, ch := range r.listeners {
		select {
		case ch <- ev:
		default:
			// drop if the listener is not keeping up
		}
	}
}
