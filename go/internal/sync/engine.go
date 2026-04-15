// Package sync implements a vector-clock–based sync engine that reconciles
// Interaction records across server and client nodes.
//
// Each node maintains a logical clock (its own counter in a shared map).
// When two nodes exchange SyncHello frames the engine compares clocks and
// streams the missing Interactions as SyncOp messages.
package sync

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// Engine tracks a node's vector clock and drives reconciliation against a
// Store-like backend.
type Engine struct {
	mu      sync.Mutex
	nodeID  string
	clock   map[string]int64
	backend Backend
}

// Backend is the minimal store interface required by the sync engine.
type Backend interface {
	ListInteractions() ([]*events.Interaction, error)
	GetInteraction(id string) (*events.Interaction, error)
	SaveInteraction(ia *events.Interaction) error
	DeleteInteraction(id string) error
}

// New creates an Engine for nodeID using the given backend.
// If nodeID is empty a random UUID is generated.
func New(nodeID string, b Backend) *Engine {
	if nodeID == "" {
		nodeID = uuid.NewString()
	}
	return &Engine{
		nodeID:  nodeID,
		clock:   map[string]int64{nodeID: 0},
		backend: b,
	}
}

// NodeID returns this node's stable identity string.
func (e *Engine) NodeID() string { return e.nodeID }

// Tick increments this node's logical clock and returns the new value.
// Call this every time the local node produces a change.
func (e *Engine) Tick() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock[e.nodeID]++
	return e.clock[e.nodeID]
}

// Merge advances the local vector clock by taking the component-wise maximum
// with the remote clock.
func (e *Engine) Merge(remote map[string]int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range remote {
		if v > e.clock[k] {
			e.clock[k] = v
		}
	}
}

// Hello builds the SyncHello frame that should be sent at the start of a sync
// session.
func (e *Engine) Hello() (events.SyncHello, error) {
	var ids []string
	if e.backend != nil {
		list, err := e.backend.ListInteractions()
		if err != nil {
			return events.SyncHello{}, err
		}
		ids = make([]string, 0, len(list))
		for _, ia := range list {
			ids = append(ids, ia.ID)
		}
	}

	e.mu.Lock()
	clk := cloneClock(e.clock)
	e.mu.Unlock()

	return events.SyncHello{
		NodeID:      e.nodeID,
		VectorClock: clk,
		Known:       ids,
	}, nil
}

// Reconcile computes the set of Interactions that the remote node (described
// by hello) is missing and returns them as SyncOp "upsert" messages.
// It also merges the remote vector clock into our own.
func (e *Engine) Reconcile(hello events.SyncHello) ([]events.SyncOp, error) {
	e.Merge(hello.VectorClock)

	if e.backend == nil {
		return nil, nil
	}

	knownByRemote := make(map[string]bool, len(hello.Known))
	for _, id := range hello.Known {
		knownByRemote[id] = true
	}

	list, err := e.backend.ListInteractions()
	if err != nil {
		return nil, err
	}

	var ops []events.SyncOp
	for _, ia := range list {
		if !knownByRemote[ia.ID] {
			ops = append(ops, events.SyncOp{Op: "upsert", Interaction: ia})
		}
	}
	return ops, nil
}

// Apply integrates a SyncOp received from a remote node into the local store.
// Returns nil immediately when no backend is configured.
func (e *Engine) Apply(op events.SyncOp) error {
	if e.backend == nil {
		return nil
	}
	switch op.Op {
	case "upsert":
		if op.Interaction == nil {
			return fmt.Errorf("upsert op missing interaction")
		}
		// Check if we have a newer version — last write wins.
		existing, err := e.backend.GetInteraction(op.Interaction.ID)
		if err == nil && existing.Version >= op.Interaction.Version {
			return nil // we already have the same or newer version
		}
		return e.backend.SaveInteraction(op.Interaction)
	case "delete":
		if op.DeleteID == "" {
			return fmt.Errorf("delete op missing delete_id")
		}
		return e.backend.DeleteInteraction(op.DeleteID)
	default:
		return fmt.Errorf("unknown sync op: %q", op.Op)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Wire helpers
// ─────────────────────────────────────────────────────────────────────────────

// MarshalHello serialises a SyncHello to JSON.
func MarshalHello(h events.SyncHello) ([]byte, error) { return json.Marshal(h) }

// UnmarshalHello deserialises JSON into a SyncHello.
func UnmarshalHello(data []byte) (events.SyncHello, error) {
	var h events.SyncHello
	return h, json.Unmarshal(data, &h)
}

// MarshalOp serialises a SyncOp to JSON.
func MarshalOp(op events.SyncOp) ([]byte, error) { return json.Marshal(op) }

// UnmarshalOp deserialises JSON into a SyncOp.
func UnmarshalOp(data []byte) (events.SyncOp, error) {
	var op events.SyncOp
	return op, json.Unmarshal(data, &op)
}

// ─────────────────────────────────────────────────────────────────────────────
// Periodic sync scheduler
// ─────────────────────────────────────────────────────────────────────────────

// SyncFunc is called periodically to perform a sync round.
type SyncFunc func() error

// Schedule starts a goroutine that calls fn every interval.
// The returned stop function cancels the scheduler.
func Schedule(interval time.Duration, fn SyncFunc) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := fn(); err != nil {
					fmt.Printf("[sync] error: %v\n", err)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

// ─────────────────────────────────────────────────────────────────────────────

func cloneClock(c map[string]int64) map[string]int64 {
	cp := make(map[string]int64, len(c))
	for k, v := range c {
		cp[k] = v
	}
	return cp
}
