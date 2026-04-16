// Package client provides an authenticated WebSocket client for the interact
// server.  It handles all three auth/signing modes transparently so callers
// never need to touch crypto primitives directly.
//
// Usage:
//
//	conn, err := client.Dial(ctx, client.Config{
//	    ServerURL:     "ws://localhost:8080",
//	    Token:         "mysecret",
//	    ChallengeAuth: true,
//	    SignFrames:    true,
//	}, "/ws/interact?mode=record&name=demo")
//	if err != nil { ... }
//	defer conn.Close()
//
//	// Send a WireEvent (signed automatically when SignFrames=true).
//	conn.Send(ev)
//
//	// Receive a WireEvent (MAC verified automatically when SignFrames=true).
//	ev, err := conn.Recv()
package client

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	icrypto "github.com/newuser-admin/claude/interact/internal/crypto"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// Config configures a Dial call.
type Config struct {
	// ServerURL is the base WebSocket URL, e.g. "ws://localhost:8080" or
	// "wss://host:8443".  Do NOT include a path.
	ServerURL string

	// Token is the shared secret used for authentication.
	Token string

	// ChallengeAuth enables the PBKDF2+HMAC challenge-response handshake.
	// The server must be started with --auth-challenge.
	ChallengeAuth bool

	// SignFrames enables HMAC-SHA256 signing of every binary wire frame.
	// The server must be started with --sign-frames.
	// When ChallengeAuth is also true the session key comes from the challenge
	// salt, so no extra round-trip is needed.  When only SignFrames is true a
	// lightweight salt-exchange preamble is performed.
	SignFrames bool

	// TLSSkipVerify disables TLS certificate verification.  Use only with
	// self-signed development certificates.
	TLSSkipVerify bool

	// DialTimeout caps the WebSocket handshake + auth round-trip.
	// Zero uses the default of 15 seconds.
	DialTimeout time.Duration
}

// Conn is an authenticated interact WebSocket connection.
// It is safe to use from a single goroutine.
type Conn struct {
	ws         *websocket.Conn
	sessionKey []byte // nil when signing is off
	signFrames bool
}

// Dial connects to path on the interact server and performs the configured
// authentication handshake.  path should include all query parameters, e.g.
// "/ws/interact?mode=record&name=demo".
func Dial(ctx context.Context, cfg Config, path string) (*Conn, error) {
	timeout := cfg.DialTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
	}
	if cfg.TLSSkipVerify {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}

	wsURL := cfg.ServerURL + path

	// For non-challenge auth the token goes in the Authorization header so it
	// is never exposed in server access logs (unlike the legacy ?token= approach).
	header := http.Header{}
	if !cfg.ChallengeAuth {
		header.Set("Authorization", "Bearer "+cfg.Token)
	}

	ws, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", wsURL, err)
	}

	c := &Conn{ws: ws, signFrames: cfg.SignFrames}

	// Perform whichever handshake the server requires.
	switch {
	case cfg.ChallengeAuth:
		key, err := c.doChallenge(cfg.Token)
		if err != nil {
			ws.Close()
			return nil, fmt.Errorf("challenge-response: %w", err)
		}
		c.sessionKey = key

	case cfg.SignFrames:
		key, err := c.doSaltExchange(cfg.Token)
		if err != nil {
			ws.Close()
			return nil, fmt.Errorf("salt exchange: %w", err)
		}
		c.sessionKey = key
	}

	return c, nil
}

// ─── Auth handshakes ──────────────────────────────────────────────────────────

// doChallenge handles the server-issued challenge-response handshake and
// returns the per-session signing key derived from the challenge's salt.
func (c *Conn) doChallenge(token string) ([]byte, error) {
	// 1. Read the challenge JSON.
	_, raw, err := c.ws.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read challenge: %w", err)
	}
	var challenge struct {
		Type  string `json:"type"`
		Nonce string `json:"nonce"`
		Salt  string `json:"salt"`
	}
	if err := json.Unmarshal(raw, &challenge); err != nil || challenge.Type != "challenge" {
		return nil, fmt.Errorf("expected challenge, got: %.80s", raw)
	}

	nonce, err := hex.DecodeString(challenge.Nonce)
	if err != nil {
		return nil, fmt.Errorf("bad nonce hex: %w", err)
	}
	salt, err := hex.DecodeString(challenge.Salt)
	if err != nil {
		return nil, fmt.Errorf("bad salt hex: %w", err)
	}

	// 2. Derive session key and HMAC the nonce.
	key := icrypto.DeriveKey([]byte(token), salt)
	response := icrypto.Sign(key, nonce)

	// 3. Send the auth response.
	authMsg, _ := json.Marshal(map[string]string{
		"type":     "auth",
		"response": hex.EncodeToString(response),
	})
	if err := c.ws.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		return nil, fmt.Errorf("send response: %w", err)
	}

	// 4. Read the server's confirmation.
	_, raw, err = c.ws.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read confirmation: %w", err)
	}
	var confirm struct{ Type string `json:"type"` }
	if json.Unmarshal(raw, &confirm) != nil || confirm.Type != "ok" {
		return nil, fmt.Errorf("authentication rejected: %.80s", raw)
	}

	return key, nil
}

// doSaltExchange reads the server's {"type":"session","salt":"<hex>"} message
// and returns the per-session signing key derived from it.
func (c *Conn) doSaltExchange(token string) ([]byte, error) {
	_, raw, err := c.ws.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read session preamble: %w", err)
	}
	var msg struct {
		Type string `json:"type"`
		Salt string `json:"salt"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "session" {
		return nil, fmt.Errorf("expected session preamble, got: %.80s", raw)
	}
	salt, err := hex.DecodeString(msg.Salt)
	if err != nil {
		return nil, fmt.Errorf("bad salt hex: %w", err)
	}
	return icrypto.DeriveKey([]byte(token), salt), nil
}

// ─── Frame I/O ────────────────────────────────────────────────────────────────

// Send serialises ev and writes it as a binary WebSocket message.
// If the connection was established with SignFrames=true the frame is
// HMAC-SHA256-signed (45 bytes); otherwise the raw 13-byte wire format is used.
func (c *Conn) Send(ev events.WireEvent) error {
	var payload []byte
	if c.signFrames && c.sessionKey != nil {
		af := icrypto.PackSigned(c.sessionKey, ev)
		payload = af[:]
	} else {
		b := events.Pack(ev)
		payload = b[:]
	}
	return c.ws.WriteMessage(websocket.BinaryMessage, payload)
}

// Recv reads the next binary WebSocket message and unpacks it as a WireEvent.
// If SignFrames is enabled the MAC is verified; a verification failure returns
// an error (the connection should be closed).
// Text messages (e.g. JSON control messages from the server) are skipped.
func (c *Conn) Recv() (events.WireEvent, error) {
	for {
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return events.WireEvent{}, err
		}
		if mt != websocket.BinaryMessage {
			// Skip JSON control messages (heartbeats, etc.).
			continue
		}
		if c.signFrames && c.sessionKey != nil {
			ev, ok := icrypto.UnpackSigned(c.sessionKey, data)
			if !ok {
				return events.WireEvent{}, fmt.Errorf("frame MAC verification failed — possible tampering")
			}
			return ev, nil
		}
		ev, ok := events.UnpackSlice(data)
		if !ok {
			return events.WireEvent{}, fmt.Errorf("short frame: %d bytes", len(data))
		}
		return ev, nil
	}
}

// ─── Retry dial ───────────────────────────────────────────────────────────────

// RetryConfig controls the behaviour of DialWithRetry.
type RetryConfig struct {
	// MaxAttempts is the total number of dial attempts (including the first).
	// 0 or 1 means one attempt only (no retries).
	MaxAttempts int
	// BaseDelay is the wait before the second attempt.
	// Subsequent waits double up to MaxDelay.
	BaseDelay time.Duration
	// MaxDelay caps the exponential back-off.
	MaxDelay time.Duration
	// OnRetry is called before each retry attempt with the attempt number
	// (1-based) and the error that triggered the retry.  May be nil.
	OnRetry func(attempt int, err error)
}

// DefaultRetry is a sensible production retry config: up to 5 attempts with
// exponential back-off starting at 1 s and capped at 30 s.
var DefaultRetry = RetryConfig{
	MaxAttempts: 5,
	BaseDelay:   time.Second,
	MaxDelay:    30 * time.Second,
}

// DialWithRetry calls Dial up to rc.MaxAttempts times with exponential
// back-off between attempts.  It returns the first successful Conn or the last
// error.  The context controls both the dial timeout on each attempt and the
// overall cancellation.
func DialWithRetry(ctx context.Context, cfg Config, path string, rc RetryConfig) (*Conn, error) {
	if rc.MaxAttempts <= 1 {
		return Dial(ctx, cfg, path)
	}
	if rc.BaseDelay <= 0 {
		rc.BaseDelay = time.Second
	}
	if rc.MaxDelay <= 0 {
		rc.MaxDelay = 30 * time.Second
	}

	delay := rc.BaseDelay
	var lastErr error
	for attempt := 1; attempt <= rc.MaxAttempts; attempt++ {
		conn, err := Dial(ctx, cfg, path)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		if attempt == rc.MaxAttempts {
			break
		}

		if rc.OnRetry != nil {
			rc.OnRetry(attempt, err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2
		if delay > rc.MaxDelay {
			delay = rc.MaxDelay
		}
	}
	return nil, fmt.Errorf("dial failed after %d attempts: %w", rc.MaxAttempts, lastErr)
}

// RawConn returns the underlying gorilla WebSocket connection after all
// authentication has been completed.  Use this for JSON-framed endpoints
// (/ws/shell, /ws/sync) that do not use the binary WireEvent wire format.
func (c *Conn) RawConn() *websocket.Conn { return c.ws }

// Close sends a clean WebSocket close frame and closes the connection.
func (c *Conn) Close() error {
	c.ws.WriteMessage( //nolint:errcheck
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
	return c.ws.Close()
}

// ─── Convenience: REST helpers ───────────────────────────────────────────────

// HTTPClient returns an *http.Client pre-configured for the server (TLS if
// applicable).  Use it for the REST API endpoints.
func HTTPClient(cfg Config) *http.Client {
	if cfg.TLSSkipVerify {
		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		}
	}
	return http.DefaultClient
}

// BearerHeader returns an http.Header with the Authorization header set.
func BearerHeader(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}
