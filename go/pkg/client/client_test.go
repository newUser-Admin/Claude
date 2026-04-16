package client_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	icrypto "github.com/newuser-admin/claude/interact/internal/crypto"
	"github.com/newuser-admin/claude/interact/pkg/client"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

const testToken = "test-client-secret"

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// newMockServer creates an httptest.Server whose single WebSocket handler runs fn.
func newMockServer(t *testing.T, fn func(ws *websocket.Conn)) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		fn(ws)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// wsBase converts an httptest.Server URL to a ws:// base URL.
func wsBase(ts *httptest.Server) string {
	return strings.Replace(ts.URL, "http://", "ws://", 1)
}

// ─── Dial ─────────────────────────────────────────────────────────────────────

func TestDial_PlainBearer(t *testing.T) {
	ts := newMockServer(t, func(ws *websocket.Conn) {
		// Accept connection; handler returns → server side closes.
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
}

func TestDial_ChallengeAuth_Success(t *testing.T) {
	salt, err := icrypto.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, icrypto.NonceLen)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	key := icrypto.DeriveKey([]byte(testToken), salt)

	ts := newMockServer(t, func(ws *websocket.Conn) {
		// 1. Send challenge.
		chMsg, _ := json.Marshal(map[string]string{
			"type":  "challenge",
			"nonce": hex.EncodeToString(nonce),
			"salt":  hex.EncodeToString(salt),
		})
		ws.WriteMessage(websocket.TextMessage, chMsg) //nolint:errcheck

		// 2. Read response.
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var authResp struct {
			Type     string `json:"type"`
			Response string `json:"response"`
		}
		if json.Unmarshal(raw, &authResp) != nil || authResp.Type != "auth" {
			ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
				websocket.FormatCloseMessage(4001, "bad auth msg"))
			return
		}
		got, _ := hex.DecodeString(authResp.Response)
		if !icrypto.Verify(key, nonce, got) {
			ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
				websocket.FormatCloseMessage(4001, "bad hmac"))
			return
		}

		// 3. Confirm.
		okMsg, _ := json.Marshal(map[string]string{"type": "ok"})
		ws.WriteMessage(websocket.TextMessage, okMsg) //nolint:errcheck

		// Keep alive until client closes.
		ws.ReadMessage() //nolint:errcheck
	})

	cfg := client.Config{
		ServerURL:     wsBase(ts),
		Token:         testToken,
		ChallengeAuth: true,
	}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("Dial with challenge-auth: %v", err)
	}
	conn.Close()
}

func TestDial_ChallengeAuth_Rejected(t *testing.T) {
	salt, _ := icrypto.NewSalt()
	nonce := make([]byte, icrypto.NonceLen)

	ts := newMockServer(t, func(ws *websocket.Conn) {
		chMsg, _ := json.Marshal(map[string]string{
			"type":  "challenge",
			"nonce": hex.EncodeToString(nonce),
			"salt":  hex.EncodeToString(salt),
		})
		ws.WriteMessage(websocket.TextMessage, chMsg) //nolint:errcheck
		ws.ReadMessage()                               //nolint:errcheck // consume client's response

		// Reject — server closes with policy violation code.
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(4001, "authentication failed"))
	})

	cfg := client.Config{
		ServerURL:     wsBase(ts),
		Token:         "wrong-token",
		ChallengeAuth: true,
	}
	_, err := client.Dial(context.Background(), cfg, "/ws")
	if err == nil {
		t.Fatal("expected error when challenge-auth is rejected")
	}
}

func TestDial_SaltExchange(t *testing.T) {
	salt, err := icrypto.NewSalt()
	if err != nil {
		t.Fatal(err)
	}

	ts := newMockServer(t, func(ws *websocket.Conn) {
		// Send session preamble.
		msg, _ := json.Marshal(map[string]string{
			"type": "session",
			"salt": hex.EncodeToString(salt),
		})
		ws.WriteMessage(websocket.TextMessage, msg) //nolint:errcheck
		ws.ReadMessage()                             //nolint:errcheck // wait for close
	})

	cfg := client.Config{
		ServerURL:  wsBase(ts),
		Token:      testToken,
		SignFrames: true,
	}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("Dial with salt exchange: %v", err)
	}
	conn.Close()
}

func TestDial_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	cfg := client.Config{
		ServerURL:   "ws://127.0.0.1:1", // unreachable
		Token:       testToken,
		DialTimeout: 50 * time.Millisecond,
	}
	_, err := client.Dial(ctx, cfg, "/ws")
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// ─── Send / Recv ──────────────────────────────────────────────────────────────

func TestSendRecv_Unsigned(t *testing.T) {
	ev := events.WireEvent{Type: events.MsgKey, T: 42, V1: float32('q')}

	ts := newMockServer(t, func(ws *websocket.Conn) {
		// Echo binary frames back.
		mt, data, err := ws.ReadMessage()
		if err != nil || mt != websocket.BinaryMessage {
			return
		}
		ws.WriteMessage(websocket.BinaryMessage, data) //nolint:errcheck
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.Send(ev); err != nil {
		t.Fatalf("send: %v", err)
	}
	got, err := conn.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if got.Type != ev.Type || got.T != ev.T || got.V1 != ev.V1 {
		t.Errorf("recv mismatch: got %+v, want %+v", got, ev)
	}
}

func TestSendRecv_Signed(t *testing.T) {
	ev := events.WireEvent{Type: events.MsgKey, T: 99, V1: float32('s')}

	salt, _ := icrypto.NewSalt()

	ts := newMockServer(t, func(ws *websocket.Conn) {
		// Salt exchange preamble.
		msg, _ := json.Marshal(map[string]string{
			"type": "session",
			"salt": hex.EncodeToString(salt),
		})
		ws.WriteMessage(websocket.TextMessage, msg) //nolint:errcheck

		// Echo any binary frame back verbatim.
		mt, data, err := ws.ReadMessage()
		if err != nil || mt != websocket.BinaryMessage {
			return
		}
		ws.WriteMessage(websocket.BinaryMessage, data) //nolint:errcheck
	})

	cfg := client.Config{
		ServerURL:  wsBase(ts),
		Token:      testToken,
		SignFrames: true,
	}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.Send(ev); err != nil {
		t.Fatalf("send signed: %v", err)
	}
	got, err := conn.Recv()
	if err != nil {
		t.Fatalf("recv signed: %v", err)
	}
	if got.Type != ev.Type || got.T != ev.T || got.V1 != ev.V1 {
		t.Errorf("signed recv mismatch: got %+v, want %+v", got, ev)
	}
}

func TestRecv_SkipsTextMessages(t *testing.T) {
	ev := events.WireEvent{Type: events.MsgKey, T: 7, V1: float32('x')}

	ts := newMockServer(t, func(ws *websocket.Conn) {
		// Send a JSON control message first, then the real binary event.
		ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"heartbeat"}`)) //nolint:errcheck
		frame := events.Pack(ev)
		ws.WriteMessage(websocket.BinaryMessage, frame[:]) //nolint:errcheck
		ws.ReadMessage()                                    //nolint:errcheck // wait for close
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	got, err := conn.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if got.Type != ev.Type || got.T != ev.T || got.V1 != ev.V1 {
		t.Errorf("got %+v, want %+v", got, ev)
	}
}

func TestSend_MultipleEvents(t *testing.T) {
	evs := []events.WireEvent{
		{Type: events.MsgKey, T: 0, V1: float32('a')},
		{Type: events.MsgKey, T: 10, V1: float32('b')},
		{Type: events.MsgKey, T: 20, V1: float32('c')},
	}

	ts := newMockServer(t, func(ws *websocket.Conn) {
		for range evs {
			mt, data, err := ws.ReadMessage()
			if err != nil || mt != websocket.BinaryMessage {
				return
			}
			ws.WriteMessage(websocket.BinaryMessage, data) //nolint:errcheck
		}
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	for i, ev := range evs {
		if err := conn.Send(ev); err != nil {
			t.Fatalf("send[%d]: %v", i, err)
		}
		got, err := conn.Recv()
		if err != nil {
			t.Fatalf("recv[%d]: %v", i, err)
		}
		if got.V1 != ev.V1 {
			t.Errorf("event[%d] V1: got %.0f, want %.0f", i, got.V1, ev.V1)
		}
	}
}

// ─── DialWithRetry ────────────────────────────────────────────────────────────

func TestDialWithRetry_SingleAttempt(t *testing.T) {
	ts := newMockServer(t, func(ws *websocket.Conn) {
		ws.ReadMessage() //nolint:errcheck
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	rc := client.RetryConfig{MaxAttempts: 1}
	conn, err := client.DialWithRetry(context.Background(), cfg, "/ws", rc)
	if err != nil {
		t.Fatalf("DialWithRetry: %v", err)
	}
	conn.Close()
}

func TestDialWithRetry_RetriesOnFailure(t *testing.T) {
	var attempts int32

	// Server that rejects the first connection attempt, accepts subsequent ones.
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		ws.ReadMessage() //nolint:errcheck
	}))
	t.Cleanup(ts.Close)

	var retryCalled int32
	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	rc := client.RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   5 * time.Millisecond,
		MaxDelay:    50 * time.Millisecond,
		OnRetry: func(attempt int, err error) {
			atomic.AddInt32(&retryCalled, 1)
		},
	}
	conn, err := client.DialWithRetry(context.Background(), cfg, "/ws", rc)
	if err != nil {
		t.Fatalf("DialWithRetry: %v", err)
	}
	conn.Close()

	if n := atomic.LoadInt32(&attempts); n < 2 {
		t.Errorf("expected ≥ 2 attempts, got %d", n)
	}
	if atomic.LoadInt32(&retryCalled) == 0 {
		t.Error("OnRetry was never called")
	}
}

func TestDialWithRetry_AllFail(t *testing.T) {
	cfg := client.Config{
		ServerURL:   "ws://127.0.0.1:1", // port 1: always refused
		Token:       testToken,
		DialTimeout: 100 * time.Millisecond,
	}
	rc := client.RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   5 * time.Millisecond,
		MaxDelay:    20 * time.Millisecond,
	}
	_, err := client.DialWithRetry(context.Background(), cfg, "/ws", rc)
	if err == nil {
		t.Fatal("expected error when all attempts fail")
	}
}

func TestDialWithRetry_ContextCancelled(t *testing.T) {
	cfg := client.Config{
		ServerURL:   "ws://127.0.0.1:1",
		Token:       testToken,
		DialTimeout: 50 * time.Millisecond,
	}
	rc := client.RetryConfig{
		MaxAttempts: 10,
		BaseDelay:   500 * time.Millisecond, // longer than ctx timeout
		MaxDelay:    time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := client.DialWithRetry(ctx, cfg, "/ws", rc)
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}

func TestDialWithRetry_ZeroMaxAttempts_TreatedAsOne(t *testing.T) {
	ts := newMockServer(t, func(ws *websocket.Conn) {
		ws.ReadMessage() //nolint:errcheck
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	// MaxAttempts=0 → implementation treats it as 1 (no retries).
	rc := client.RetryConfig{MaxAttempts: 0}
	conn, err := client.DialWithRetry(context.Background(), cfg, "/ws", rc)
	if err != nil {
		t.Fatalf("DialWithRetry: %v", err)
	}
	conn.Close()
}

// ─── RawConn ──────────────────────────────────────────────────────────────────

func TestRawConn_NotNil(t *testing.T) {
	ts := newMockServer(t, func(ws *websocket.Conn) {
		ws.ReadMessage() //nolint:errcheck
	})

	cfg := client.Config{ServerURL: wsBase(ts), Token: testToken}
	conn, err := client.Dial(context.Background(), cfg, "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	raw := conn.RawConn()
	if raw == nil {
		t.Fatal("RawConn() returned nil")
	}
}

// ─── Convenience helpers ─────────────────────────────────────────────────────

func TestHTTPClient_NotNil(t *testing.T) {
	hc := client.HTTPClient(client.Config{})
	if hc == nil {
		t.Fatal("HTTPClient returned nil")
	}
}

func TestHTTPClient_TLSSkipVerify(t *testing.T) {
	hc := client.HTTPClient(client.Config{TLSSkipVerify: true})
	if hc == nil {
		t.Fatal("HTTPClient(TLSSkipVerify=true) returned nil")
	}
}

func TestBearerHeader(t *testing.T) {
	h := client.BearerHeader("my-token")
	if auth := h.Get("Authorization"); auth != "Bearer my-token" {
		t.Errorf("got %q, want %q", auth, "Bearer my-token")
	}
}

func TestBearerHeader_Empty(t *testing.T) {
	h := client.BearerHeader("")
	if auth := h.Get("Authorization"); auth != "Bearer " {
		t.Errorf("got %q, want \"Bearer \"", auth)
	}
}
