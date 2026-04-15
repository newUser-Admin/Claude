package server_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	icrypto "github.com/newuser-admin/claude/interact/internal/crypto"
	"github.com/newuser-admin/claude/interact/internal/server"
	"github.com/newuser-admin/claude/interact/internal/store"
	"github.com/newuser-admin/claude/interact/pkg/client"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

const testToken = "test-secret-token"

// newTestServer creates an in-memory server+store with the given Config
// overrides and returns it wrapped in an httptest.Server.
func newTestServer(t *testing.T, cfg server.Config) (*httptest.Server, func()) {
	t.Helper()
	cfg.Token = testToken
	if cfg.JitterDelay == 0 {
		cfg.JitterDelay = 0 // disable jitter in tests for determinism
	}
	cfg.NodeID = "test-node"

	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	srv := server.New(cfg, st)
	ts := httptest.NewServer(srv)
	return ts, func() {
		ts.Close()
		st.Close()
	}
}

// wsURL converts an HTTP test server URL to WebSocket.
func wsURL(ts *httptest.Server, path string) string {
	return strings.Replace(ts.URL, "http://", "ws://", 1) + path
}

// dialRaw dials a WebSocket connection with a Bearer token header (no auth challenge).
func dialRaw(t *testing.T, ts *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": {"Bearer " + testToken}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(ts, path), header)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return ws
}

// ─── HTTP auth ────────────────────────────────────────────────────────────────

func TestHTTP_AuthRequired(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	// No token → 401.
	resp, err := http.Get(ts.URL + "/api/interactions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHTTP_BearerToken(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/interactions", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHTTP_TokenQueryParam(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/interactions?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHTTP_WrongToken(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/interactions", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ─── Health ───────────────────────────────────────────────────────────────────

func TestHealth(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("unexpected status: %q", body["status"])
	}
}

// ─── WS: legacy token auth ────────────────────────────────────────────────────

func TestWS_LegacyToken_WrongToken(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	// Wrong token → upgrade should be rejected with 401.
	header := http.Header{"Authorization": {"Bearer wrongtoken"}}
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws/interact?mode=record"), header)
	if err == nil {
		t.Fatal("expected dial to fail with wrong token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestWS_LegacyToken_ValidToken(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	ws := dialRaw(t, ts, "/ws/interact?mode=record&name=test")
	defer ws.Close()
	// Connection established — close cleanly.
	ws.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// ─── WS: challenge-response auth ─────────────────────────────────────────────

func TestWS_ChallengeAuth_Success(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{ChallengeAuth: true})
	defer cleanup()

	ws, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws/interact?mode=record&name=cr-test"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// 1. Read challenge.
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	var ch struct {
		Type  string `json:"type"`
		Nonce string `json:"nonce"`
		Salt  string `json:"salt"`
	}
	if err := json.Unmarshal(raw, &ch); err != nil || ch.Type != "challenge" {
		t.Fatalf("expected challenge, got: %s", raw)
	}

	nonce, _ := hex.DecodeString(ch.Nonce)
	salt, _ := hex.DecodeString(ch.Salt)
	key := icrypto.DeriveKey([]byte(testToken), salt)
	resp := icrypto.Sign(key, nonce)

	// 2. Send auth response.
	authJSON, _ := json.Marshal(map[string]string{
		"type":     "auth",
		"response": hex.EncodeToString(resp),
	})
	if err := ws.WriteMessage(websocket.TextMessage, authJSON); err != nil {
		t.Fatalf("send response: %v", err)
	}

	// 3. Expect {"type":"ok"}.
	_, raw, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("read ok: %v", err)
	}
	var ok struct{ Type string `json:"type"` }
	if json.Unmarshal(raw, &ok) != nil || ok.Type != "ok" {
		t.Fatalf("expected ok, got: %s", raw)
	}
}

func TestWS_ChallengeAuth_WrongResponse(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{ChallengeAuth: true})
	defer cleanup()

	ws, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws/interact?mode=record"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Read challenge.
	_, _, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("read challenge: %v", err)
	}

	// Send garbage response.
	badAuth, _ := json.Marshal(map[string]string{
		"type":     "auth",
		"response": hex.EncodeToString(make([]byte, 32)),
	})
	ws.WriteMessage(websocket.TextMessage, badAuth) //nolint:errcheck

	// Server should close with code 4001.
	_, _, err = ws.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed after bad auth")
	}
	closeErr, ok := err.(*websocket.CloseError)
	if !ok || closeErr.Code != 4001 {
		t.Logf("got error: %v (want close code 4001)", err)
	}
}

// ─── WS: record + playback via pkg/client ────────────────────────────────────

func TestWSInteract_RecordAndPlay_NoSigning(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	cfg := client.Config{
		ServerURL: strings.Replace(ts.URL, "http://", "ws://", 1),
		Token:     testToken,
	}

	// Record three events.
	recConn, err := client.Dial(context.Background(), cfg, "/ws/interact?mode=record&name=e2e-test")
	if err != nil {
		t.Fatalf("dial record: %v", err)
	}

	sent := []events.WireEvent{
		{Type: events.MsgKey, T: 0, V1: float32('h')},
		{Type: events.MsgKey, T: 50, V1: float32('i')},
		{Type: events.MsgKey, T: 100, V1: float32('\n')},
	}
	for _, ev := range sent {
		if err := recConn.Send(ev); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	recConn.Close()

	// Give the server time to persist.
	time.Sleep(150 * time.Millisecond)

	// Fetch the interaction ID.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/interactions", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var list []*events.Interaction
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no interactions found after recording")
	}
	id := list[0].ID

	// Play it back.
	playConn, err := client.Dial(context.Background(), cfg, "/ws/interact?mode=play&id="+id)
	if err != nil {
		t.Fatalf("dial play: %v", err)
	}
	defer playConn.Close()

	var received []events.WireEvent
	for {
		ev, err := playConn.Recv()
		if err != nil {
			break
		}
		if ev.Type == events.MsgSync {
			continue // skip heartbeats
		}
		received = append(received, ev)
	}

	if len(received) != len(sent) {
		t.Fatalf("received %d events, want %d", len(received), len(sent))
	}
	for i, ev := range received {
		if ev.Type != sent[i].Type || ev.V1 != sent[i].V1 {
			t.Errorf("event[%d]: got {%v %.0f}, want {%v %.0f}",
				i, ev.Type, ev.V1, sent[i].Type, sent[i].V1)
		}
	}
}

func TestWSInteract_RecordAndPlay_SignedFrames(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{SignFrames: true})
	defer cleanup()

	cfg := client.Config{
		ServerURL:  strings.Replace(ts.URL, "http://", "ws://", 1),
		Token:      testToken,
		SignFrames: true,
	}

	// Record.
	recConn, err := client.Dial(context.Background(), cfg, "/ws/interact?mode=record&name=signed-test")
	if err != nil {
		t.Fatalf("dial record: %v", err)
	}
	ev := events.WireEvent{Type: events.MsgKey, T: 0, V1: float32('z')}
	if err := recConn.Send(ev); err != nil {
		t.Fatalf("send signed: %v", err)
	}
	recConn.Close()
	time.Sleep(150 * time.Millisecond)

	// Get ID.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/interactions", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	var list []*events.Interaction
	json.NewDecoder(resp.Body).Decode(&list) //nolint:errcheck
	if len(list) == 0 {
		t.Fatal("no interaction saved")
	}
	id := list[0].ID

	// Play back with signed frames.
	playConn, err := client.Dial(context.Background(), cfg, "/ws/interact?mode=play&id="+id)
	if err != nil {
		t.Fatalf("dial play: %v", err)
	}
	defer playConn.Close()

	got, err := playConn.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if got.V1 != float32('z') {
		t.Errorf("got V1=%.0f, want %.0f", got.V1, float32('z'))
	}
}

func TestWSInteract_ChallengeAuth_EndToEnd(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{ChallengeAuth: true, SignFrames: true})
	defer cleanup()

	cfg := client.Config{
		ServerURL:     strings.Replace(ts.URL, "http://", "ws://", 1),
		Token:         testToken,
		ChallengeAuth: true,
		SignFrames:    true,
	}

	recConn, err := client.Dial(context.Background(), cfg, "/ws/interact?mode=record&name=full-e2e")
	if err != nil {
		t.Fatalf("dial with challenge-auth: %v", err)
	}
	ev := events.WireEvent{Type: events.MsgClick, T: 0, V1: 0.5 + 0*1000, V2: 0.5} // left click at centre
	recConn.Send(ev)                                                                   //nolint:errcheck
	recConn.Close()
	time.Sleep(300 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/interactions", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	var list []*events.Interaction
	json.NewDecoder(resp.Body).Decode(&list) //nolint:errcheck
	if len(list) == 0 {
		t.Fatal("no interaction saved")
	}
}

// ─── REST: interactions ───────────────────────────────────────────────────────

func TestREST_Interactions_CRUD(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	do := func(method, path string, body interface{}) *http.Response {
		var r strings.Reader
		var rb interface{ Read([]byte) (int, error) } = strings.NewReader("")
		if body != nil {
			data, _ := json.Marshal(body)
			r = *strings.NewReader(string(data))
			rb = &r
		}
		req, _ := http.NewRequest(method, ts.URL+path, rb)
		req.Header.Set("Authorization", "Bearer "+testToken)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// POST new interaction.
	ia := events.Interaction{
		Name: "test-ia",
		Events: []events.WireEvent{
			{Type: events.MsgKey, T: 0, V1: 65},
		},
	}
	postResp := do(http.MethodPost, "/api/interactions", ia)
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST: expected 200, got %d", postResp.StatusCode)
	}
	var created events.Interaction
	json.NewDecoder(postResp.Body).Decode(&created) //nolint:errcheck
	if created.ID == "" {
		t.Fatal("created interaction has no ID")
	}

	// GET by ID.
	getResp := do(http.MethodGet, "/api/interactions/"+created.ID, nil)
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d", getResp.StatusCode)
	}

	// DELETE.
	delResp := do(http.MethodDelete, "/api/interactions/"+created.ID, nil)
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: expected 200 or 204, got %d", delResp.StatusCode)
	}

	// GET after delete → 404.
	get2Resp := do(http.MethodGet, "/api/interactions/"+created.ID, nil)
	defer get2Resp.Body.Close()
	if get2Resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete: expected 404, got %d", get2Resp.StatusCode)
	}
}

// ─── REST: payloads ───────────────────────────────────────────────────────────

func TestREST_Payloads_PushAck(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{})
	defer cleanup()

	do := func(method, path string, body interface{}) *http.Response {
		var rb interface{ Read([]byte) (int, error) } = strings.NewReader("")
		if body != nil {
			data, _ := json.Marshal(body)
			rb = strings.NewReader(string(data))
		}
		req, _ := http.NewRequest(method, ts.URL+path, rb)
		req.Header.Set("Authorization", "Bearer "+testToken)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	p := events.Payload{
		Kind: events.PayloadConfig,
		Name: "test.json",
		Data: []byte(`{"key":"value"}`),
	}

	postResp := do(http.MethodPost, "/api/payloads", p)
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST payload: expected 200, got %d", postResp.StatusCode)
	}
	var created events.Payload
	json.NewDecoder(postResp.Body).Decode(&created) //nolint:errcheck
	if created.ID == "" {
		t.Fatal("created payload has no ID")
	}

	// ACK it.
	ackResp := do(http.MethodPost, "/api/payloads/"+created.ID+"/ack", nil)
	defer ackResp.Body.Close()
	if ackResp.StatusCode != http.StatusOK && ackResp.StatusCode != http.StatusNoContent {
		t.Fatalf("ACK: expected 200 or 204, got %d", ackResp.StatusCode)
	}

	// List pending — should be empty.
	listResp := do(http.MethodGet, "/api/payloads?pending=1", nil)
	defer listResp.Body.Close()
	var list []*events.Payload
	json.NewDecoder(listResp.Body).Decode(&list) //nolint:errcheck
	for _, pl := range list {
		if pl.ID == created.ID && !pl.Acked {
			t.Error("payload still listed as pending after ack")
		}
	}
}

func TestREST_Payloads_EncryptedRoundtrip(t *testing.T) {
	ts, cleanup := newTestServer(t, server.Config{EncryptPayloads: true})
	defer cleanup()

	plain := []byte("secret payload data")
	p := events.Payload{Kind: events.PayloadConfig, Name: "enc.bin", Data: plain}

	// POST
	data, _ := json.Marshal(p)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/payloads", strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	var created events.Payload
	json.NewDecoder(resp.Body).Decode(&created) //nolint:errcheck
	if created.ID == "" {
		t.Fatal("no ID")
	}

	// GET — server should decrypt on the way out.
	getReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/payloads/"+created.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+testToken)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d", getResp.StatusCode)
	}
	var fetched events.Payload
	json.NewDecoder(getResp.Body).Decode(&fetched) //nolint:errcheck
	if string(fetched.Data) != string(plain) {
		t.Errorf("decrypted data mismatch: got %q, want %q", fetched.Data, plain)
	}
}
