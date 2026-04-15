// Package events defines the wire protocol and higher-level interaction types
// for the interact system.
//
// Wire format (13 bytes, big-endian) — compatible with interaction-engine.js:
//
//	[0]     uint8   message type
//	[1..4]  uint32  relative timestamp (ms from recording start)
//	[5..8]  float32 v1  (keyCode OR x-fraction for clicks)
//	[9..12] float32 v2  (y-fraction for clicks; 0 for key events)
package events

import (
	"encoding/binary"
	"math"
	"time"
)

// MsgType is the first byte of every wire frame.
type MsgType uint8

const (
	MsgKey    MsgType = 0 // keyboard event  — v1=keyCode, v2=0
	MsgClick  MsgType = 1 // mouse click     — v1=xFrac,   v2=yFrac
	MsgSync   MsgType = 2 // sync/heartbeat  — v1=seqNo,   v2=0
	MsgScroll MsgType = 3 // scroll event    — v1=deltaX,  v2=deltaY
)

// WireLen is the fixed size of every wire frame in bytes.
const WireLen = 13

// WireEvent is the in-memory representation of one 13-byte frame.
type WireEvent struct {
	Type MsgType
	T    uint32  // relative timestamp in milliseconds
	V1   float32 // primary value (keyCode or x-fraction)
	V2   float32 // secondary value (y-fraction or 0)
}

// Pack serialises e into a fixed-size [WireLen]byte array.
func Pack(e WireEvent) [WireLen]byte {
	var b [WireLen]byte
	b[0] = byte(e.Type)
	binary.BigEndian.PutUint32(b[1:5], e.T)
	binary.BigEndian.PutUint32(b[5:9], math.Float32bits(e.V1))
	binary.BigEndian.PutUint32(b[9:13], math.Float32bits(e.V2))
	return b
}

// Unpack deserialises a [WireLen]byte array into a WireEvent.
func Unpack(b [WireLen]byte) WireEvent {
	return WireEvent{
		Type: MsgType(b[0]),
		T:    binary.BigEndian.Uint32(b[1:5]),
		V1:   math.Float32frombits(binary.BigEndian.Uint32(b[5:9])),
		V2:   math.Float32frombits(binary.BigEndian.Uint32(b[9:13])),
	}
}

// DecodeClick extracts the normalised position and button index from a
// MsgClick WireEvent.
//
// The capture backends encode clicks as:
//
//	V1 = xFraction + buttonIndex*1000
//	V2 = yFraction
//
// where buttonIndex is 0=left, 1=right, 2=middle.
// DecodeClick reverses this into separate (x, y, button) values.
func DecodeClick(v1, v2 float32) (x, y float32, button int) {
	button = int(v1) / 1000
	x = v1 - float32(button)*1000
	y = v2
	return
}

// UnpackSlice is a convenience wrapper for []byte input.
// Returns false if len(b) < WireLen.
func UnpackSlice(b []byte) (WireEvent, bool) {
	if len(b) < WireLen {
		return WireEvent{}, false
	}
	var arr [WireLen]byte
	copy(arr[:], b[:WireLen])
	return Unpack(arr), true
}

// ─────────────────────────────────────────────────────────────
// Higher-level domain types
// ─────────────────────────────────────────────────────────────

// Interaction is a named, persisted sequence of WireEvents.
type Interaction struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Events      []WireEvent       `json:"events"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Version is a monotonically increasing counter used by the sync engine.
	Version int64 `json:"version"`
}

// PayloadKind identifies the nature of a remote payload.
type PayloadKind uint8

const (
	PayloadConfig      PayloadKind = 0 // structured configuration data
	PayloadEventStream PayloadKind = 1 // serialised WireEvent sequence
	PayloadPatch       PayloadKind = 2 // binary patch / software update
)

func (k PayloadKind) String() string {
	switch k {
	case PayloadConfig:
		return "config"
	case PayloadEventStream:
		return "event-stream"
	case PayloadPatch:
		return "patch"
	default:
		return "unknown"
	}
}

// Payload is a unit of remote delivery pushed from server to clients.
type Payload struct {
	ID        string            `json:"id"`
	Kind      PayloadKind       `json:"kind"`
	Name      string            `json:"name"`
	Data      []byte            `json:"data"`
	Checksum  string            `json:"checksum"` // hex-encoded SHA-256
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Acked     bool              `json:"acked"`
}

// ─────────────────────────────────────────────────────────────
// Sync types
// ─────────────────────────────────────────────────────────────

// SyncHello is exchanged at the start of every sync session so each side
// advertises its node identity and current vector clock.
type SyncHello struct {
	NodeID      string           `json:"node_id"`
	VectorClock map[string]int64 `json:"vector_clock"`
	// Known is a deduplicated list of interaction IDs the sender holds.
	Known []string `json:"known"`
}

// SyncOp is a single change streamed during sync.
type SyncOp struct {
	// Op is one of "upsert" or "delete".
	Op          string       `json:"op"`
	Interaction *Interaction `json:"interaction,omitempty"`
	DeleteID    string       `json:"delete_id,omitempty"`
}

// ─────────────────────────────────────────────────────────────
// WebSocket shell protocol types (mirrors shell-server.js)
// ─────────────────────────────────────────────────────────────

// ShellMsg is a framed JSON message on the /ws/shell endpoint.
type ShellMsg struct {
	// Type is one of: "input", "resize", "output", "exit", "error"
	Type string `json:"type"`

	// Payload fields (only the relevant ones are set per type)
	Data    string `json:"data,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
