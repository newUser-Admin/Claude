// Package jitter implements a priority-queue smoothing buffer that delays
// dispatch of incoming events by a fixed window — a direct port of the
// JitterBuffer class in interaction-engine.js.
//
// Instead of requestAnimationFrame the Go implementation drives the tick via a
// background goroutine that fires at a configurable poll interval.
package jitter

import (
	"container/heap"
	"sync"
	"time"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// entry is an event scheduled for future dispatch.
type entry struct {
	event  events.WireEvent
	playAt time.Time
	index  int // position inside the heap (maintained by heap.Interface)
}

// entryHeap is a min-heap ordered by playAt.
type entryHeap []*entry

func (h entryHeap) Len() int            { return len(h) }
func (h entryHeap) Less(i, j int) bool  { return h[i].playAt.Before(h[j].playAt) }
func (h entryHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *entryHeap) Push(x interface{}) { e := x.(*entry); e.index = len(*h); *h = append(*h, e) }
func (h *entryHeap) Pop() interface{}   { old := *h; n := len(old); e := old[n-1]; *h = old[:n-1]; return e }

// Buffer absorbs network jitter by delaying event dispatch by a fixed window.
// Dispatch is driven by the Tick method; call it from a goroutine on a tight
// loop or use Run for a managed background dispatcher.
type Buffer struct {
	mu    sync.Mutex
	queue entryHeap
	delay time.Duration
}

// New creates a Buffer with the given smoothing delay.
// A delay of 100 ms matches the default in interaction-engine.js.
func New(delay time.Duration) *Buffer {
	b := &Buffer{delay: delay}
	heap.Init(&b.queue)
	return b
}

// Push enqueues an event for future playback.
// Safe to call from multiple goroutines.
func (b *Buffer) Push(ev events.WireEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	heap.Push(&b.queue, &entry{event: ev, playAt: time.Now().Add(b.delay)})
}

// Tick drains all events whose scheduled time has passed and dispatches them
// in chronological order via cb. Safe to call concurrently but normally called
// from a single goroutine.
func (b *Buffer) Tick(cb func(events.WireEvent)) {
	now := time.Now()
	b.mu.Lock()
	for b.queue.Len() > 0 && !b.queue[0].playAt.After(now) {
		e := heap.Pop(&b.queue).(*entry)
		b.mu.Unlock()
		cb(e.event)
		b.mu.Lock()
	}
	b.mu.Unlock()
}

// Flush discards all buffered events.
func (b *Buffer) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = b.queue[:0]
	heap.Init(&b.queue)
}

// Size returns the number of events currently waiting.
func (b *Buffer) Size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.queue.Len()
}

// Run starts a background goroutine that calls Tick at pollInterval and
// dispatches ready events to cb. It exits when the returned stop function is
// called.
func (b *Buffer) Run(pollInterval time.Duration, cb func(events.WireEvent)) (stop func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b.Tick(cb)
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
