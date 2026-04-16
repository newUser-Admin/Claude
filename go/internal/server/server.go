// Package server exposes the full interact API over HTTP and WebSocket.
//
// Endpoints
//
//	REST
//	  POST   /api/interactions          – create / update an interaction
//	  GET    /api/interactions          – list all interactions
//	  GET    /api/interactions/{id}     – fetch one interaction
//	  DELETE /api/interactions/{id}     – delete one interaction
//	  POST   /api/payloads              – push a payload (JSON body with base64 data)
//	  GET    /api/payloads              – list payloads (?pending=1 for un-acked only)
//	  POST   /api/payloads/{id}/ack     – acknowledge a payload
//
//	WebSocket
//	  /ws/interact  – binary frames (WireEvent, 13 bytes each); works as both
//	                  recorder (server stores incoming events) and playback
//	                  (server streams stored events to client).
//	  /ws/shell     – JSON frames; full PTY shell session (same protocol as
//	                  shell-server.js so shell-client.html connects unmodified).
//	  /ws/sync      – JSON frames; bidirectional vector-clock sync session.
//
// Authentication
//
//	All endpoints require a Bearer token via the Authorization header or the
//	?token= query parameter (WebSocket connections typically use the latter).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	icrypto "github.com/newuser-admin/claude/interact/internal/crypto"
	"github.com/newuser-admin/claude/interact/internal/jitter"
	shellpty "github.com/newuser-admin/claude/interact/internal/pty"
	"github.com/newuser-admin/claude/interact/internal/store"
	isync "github.com/newuser-admin/claude/interact/internal/sync"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// Config holds server-wide settings.
type Config struct {
	Token           string        // shared secret for auth
	JitterDelay     time.Duration // jitter buffer window for /ws/interact (default 100ms)
	NodeID          string        // this server's sync identity
	ChallengeAuth   bool          // use challenge-response on WS instead of token-in-URL
	SignFrames      bool          // HMAC-sign binary wire frames (45 bytes instead of 13)
	EncryptPayloads bool          // AES-256-GCM encrypt payload data at rest
	// RateLimit is the sustained per-IP request rate (requests/second).
	// 0 means unlimited.
	RateLimit float64
	// RateBurst is the per-IP burst allowance.  Defaults to max(1, RateLimit*3).
	RateBurst int
}

// Server is the main HTTP + WebSocket handler.
type Server struct {
	cfg      Config
	store    *store.Store
	sync     *isync.Engine
	upgrader websocket.Upgrader
	mux      *http.ServeMux

	// Graceful shutdown: wsWg tracks active WS handler goroutines;
	// shutCtx is cancelled when Shutdown is called.
	wsWg    sync.WaitGroup
	shutCtx context.Context
	shutFn  context.CancelFunc

	// Rate limiter (initialised in New).
	limiter *ipLimiter
}

// New wires up all handlers and returns a ready Server.
func New(cfg Config, st *store.Store) *Server {
	if cfg.JitterDelay == 0 {
		cfg.JitterDelay = 100 * time.Millisecond
	}
	shutCtx, shutFn := context.WithCancel(context.Background())
	s := &Server{
		cfg:   cfg,
		store: st,
		sync:  isync.New(cfg.NodeID, st),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		shutCtx: shutCtx,
		shutFn:  shutFn,
		limiter: newIPLimiter(cfg.RateLimit, cfg.RateBurst),
	}
	s.mux = http.NewServeMux()
	// All routes wrapped with the rate limiter.
	rl := s.limitMiddleware
	s.mux.Handle("/api/interactions", rl(s.auth(s.handleInteractions)))
	s.mux.Handle("/api/interactions/", rl(s.auth(s.handleInteraction)))
	s.mux.Handle("/api/payloads", rl(s.auth(s.handlePayloads)))
	s.mux.Handle("/api/payloads/", rl(s.auth(s.handlePayload)))
	s.mux.HandleFunc("/ws/interact", func(w http.ResponseWriter, r *http.Request) {
		s.wsAuth(w, r, func(ws *websocket.Conn, key []byte) { s.serveWSInteract(ws, r, key) })
	})
	s.mux.HandleFunc("/ws/shell", func(w http.ResponseWriter, r *http.Request) {
		s.wsAuth(w, r, func(ws *websocket.Conn, _ []byte) {
			fmt.Printf("[shell] client connected: %s\n", r.RemoteAddr)
			shellpty.Handler(ws, r.RemoteAddr)
		})
	})
	s.mux.HandleFunc("/ws/sync", func(w http.ResponseWriter, r *http.Request) {
		s.wsAuth(w, r, func(ws *websocket.Conn, _ []byte) { s.serveWSSync(ws, r) })
	})
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "node": cfg.NodeID})
	})
	return s
}

// Shutdown signals all active WebSocket handlers to wrap up and waits for them
// to finish.  ctx caps the drain wait; callers should also call
// http.Server.Shutdown to stop accepting new connections.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutFn() // signal handlers to exit their read loops
	done := make(chan struct{})
	go func() { s.wsWg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ServeHTTP implements http.Handler so the server can be passed directly to
// http.ListenAndServe / http.ListenAndServeTLS.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth middleware
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := tokenFromRequest(r)
		if token != s.cfg.Token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func tokenFromRequest(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// REST: /api/interactions
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleInteractions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListInteractions()
		if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		jsonOK(w, list)

	case http.MethodPost:
		var ia events.Interaction
		if err := jsonBody(r, &ia); err != nil {
			jsonErr(w, err, http.StatusBadRequest)
			return
		}
		if err := s.store.SaveInteraction(&ia); err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		s.sync.Tick()
		jsonOK(w, ia)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleInteraction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/interactions/")
	id = strings.TrimSuffix(id, "/ack")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		ia, err := s.store.GetInteraction(id)
		if err == store.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		} else if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		jsonOK(w, ia)

	case http.MethodDelete:
		err := s.store.DeleteInteraction(id)
		if err == store.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		} else if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		s.sync.Tick()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// REST: /api/payloads
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handlePayloads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pending := r.URL.Query().Get("pending") == "1"
		list, err := s.store.ListPayloads(pending)
		if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		jsonOK(w, list)

	case http.MethodPost:
		var p events.Payload
		if err := jsonBody(r, &p); err != nil {
			jsonErr(w, err, http.StatusBadRequest)
			return
		}
		if s.cfg.EncryptPayloads {
			if err := icrypto.EncryptPayload(&p, s.cfg.Token); err != nil {
				jsonErr(w, err, http.StatusInternalServerError)
				return
			}
		}
		if err := s.store.SavePayload(&p); err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		// Return the cleartext envelope (sans raw data) to the caller.
		p.Data = nil
		jsonOK(w, p)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePayload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/payloads/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	if action == "ack" && r.Method == http.MethodPost {
		if err := s.store.AckPayload(id); err == store.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		} else if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method == http.MethodGet {
		p, err := s.store.GetPayload(id)
		if err == store.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		} else if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		if s.cfg.EncryptPayloads {
			if err := icrypto.DecryptPayload(p, s.cfg.Token); err != nil {
				jsonErr(w, err, http.StatusInternalServerError)
				return
			}
		}
		jsonOK(w, p)
		return
	}

	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// ─────────────────────────────────────────────────────────────────────────────
// WebSocket: /ws/interact  (binary — WireEvent recording / playback)
// ─────────────────────────────────────────────────────────────────────────────

// serveWSInteract is the entry-point for /ws/interact.
// sessionKey is the per-connection HMAC key (nil when --sign-frames is off).
func (s *Server) serveWSInteract(ws *websocket.Conn, r *http.Request, sessionKey []byte) {
	remote := r.RemoteAddr
	fmt.Printf("[interact] client connected: %s\n", remote)
	defer fmt.Printf("[interact] client disconnected: %s\n", remote)

	mode := r.URL.Query().Get("mode")
	name := r.URL.Query().Get("name")
	id   := r.URL.Query().Get("id")

	switch mode {
	case "play":
		s.wsPlayback(ws, id, sessionKey)
	default:
		s.wsRecord(ws, name, sessionKey)
	}
}

// wsRecord receives binary WireEvent frames from the client, buffers them
// through the jitter filter, and persists a named Interaction on close.
// sessionKey is the per-connection HMAC key supplied by wsAuth (nil when
// --sign-frames is off); invalid MACs are silently dropped.
func (s *Server) wsRecord(ws *websocket.Conn, name string, sessionKey []byte) {
	if name == "" {
		name = fmt.Sprintf("recording-%s", time.Now().Format("20060102-150405"))
	}

	jb := jitter.New(s.cfg.JitterDelay)
	var captured []events.WireEvent

	stopJitter := jb.Run(5*time.Millisecond, func(ev events.WireEvent) {
		captured = append(captured, ev)
	})

	// Set a read deadline so we can check for shutdown periodically.
	const readPoll = 2 * time.Second
	for {
		select {
		case <-s.shutCtx.Done():
			goto drain
		default:
		}
		ws.SetReadDeadline(time.Now().Add(readPoll)) //nolint:errcheck
		_, data, err := ws.ReadMessage()
		if err != nil {
			// A timeout just means no data arrived; check shutdown and retry.
			if isTimeout(err) {
				continue
			}
			break
		}
		ws.SetReadDeadline(time.Time{}) //nolint:errcheck // clear deadline after success
		var ev events.WireEvent
		var ok bool
		if s.cfg.SignFrames {
			ev, ok = icrypto.UnpackSigned(sessionKey, data)
		} else {
			ev, ok = events.UnpackSlice(data)
		}
		if !ok {
			continue
		}
		jb.Push(ev)
	}

drain:
	// Stop the background jitter goroutine before the final drain.
	stopJitter()
	if s.cfg.JitterDelay > 0 {
		time.Sleep(s.cfg.JitterDelay + 10*time.Millisecond)
	}
	jb.Tick(func(ev events.WireEvent) { captured = append(captured, ev) })

	if len(captured) == 0 {
		return
	}

	ia := &events.Interaction{Name: name, Events: captured}
	if err := s.store.SaveInteraction(ia); err != nil {
		fmt.Printf("[interact] save error: %v\n", err)
		return
	}
	fmt.Printf("[interact] saved %q (%d events)\n", ia.Name, len(ia.Events))
	s.sync.Tick()
}

// heartbeatInterval is how often MsgSync frames are injected between events
// during a long playback to keep the connection alive and let the client
// detect dropped connections.
const heartbeatInterval = 30 * time.Second

// wsPlayback loads an Interaction by id and streams its WireEvents as binary
// frames at their original relative timing.
// sessionKey is the per-connection HMAC key supplied by wsAuth (nil when
// --sign-frames is off).
//
// A MsgSync frame (V1 = sequence number) is injected every heartbeatInterval
// of wall time between events so the client can detect a stale connection.
func (s *Server) wsPlayback(ws *websocket.Conn, id string, sessionKey []byte) {
	ia, err := s.store.GetInteraction(id)
	if err != nil {
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(4004, "interaction not found"))
		return
	}

	sendFrame := func(ev events.WireEvent) error {
		var payload []byte
		if s.cfg.SignFrames && sessionKey != nil {
			af := icrypto.PackSigned(sessionKey, ev)
			payload = af[:]
		} else {
			b := events.Pack(ev)
			payload = b[:]
		}
		return ws.WriteMessage(websocket.BinaryMessage, payload)
	}

	start := time.Now()
	lastHeartbeat := start
	var seq uint32

	for _, ev := range ia.Events {
		target := start.Add(time.Duration(ev.T) * time.Millisecond)

		// Inject heartbeat(s) if the gap to the next event is long.
		for time.Until(target) > heartbeatInterval {
			time.Sleep(heartbeatInterval)
			lastHeartbeat = time.Now()
			seq++
			hb := events.WireEvent{
				Type: events.MsgSync,
				T:    uint32(time.Since(start).Milliseconds()),
				V1:   float32(seq),
			}
			if err := sendFrame(hb); err != nil {
				return
			}
		}

		// Sleep only if a heartbeat hasn't already consumed the wait.
		if wait := time.Until(target); wait > 0 {
			time.Sleep(wait)
		}
		_ = lastHeartbeat

		if err := sendFrame(ev); err != nil {
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ─────────────────────────────────────────────────────────────────────────────
// WebSocket: /ws/sync  (JSON bidirectional sync)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) serveWSSync(ws *websocket.Conn, r *http.Request) {
	remote := r.RemoteAddr
	fmt.Printf("[sync] session with %s\n", remote)

	// Step 1: send our Hello.
	hello, err := s.sync.Hello()
	if err != nil {
		fmt.Printf("[sync] hello error: %v\n", err)
		return
	}
	data, _ := isync.MarshalHello(hello)
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		return
	}

	// Step 2: receive remote Hello.
	_, raw, err := ws.ReadMessage()
	if err != nil {
		return
	}
	remoteHello, err := isync.UnmarshalHello(raw)
	if err != nil {
		fmt.Printf("[sync] bad hello from %s: %v\n", remote, err)
		return
	}

	// Step 3: compute and stream ops to the remote node.
	ops, err := s.sync.Reconcile(remoteHello)
	if err != nil {
		fmt.Printf("[sync] reconcile error: %v\n", err)
		return
	}
	for _, op := range ops {
		d, _ := isync.MarshalOp(op)
		if err := ws.WriteMessage(websocket.TextMessage, d); err != nil {
			return
		}
	}
	// Signal end of our stream with an empty JSON object.
	ws.WriteMessage(websocket.TextMessage, []byte("{}")) //nolint:errcheck

	// Step 4: receive ops from the remote node and apply them.
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if string(raw) == "{}" {
			break // remote is done
		}
		op, err := isync.UnmarshalOp(raw)
		if err != nil {
			continue
		}
		if err := s.sync.Apply(op); err != nil {
			fmt.Printf("[sync] apply error: %v\n", err)
		}
	}

	fmt.Printf("[sync] session complete with %s\n", remote)
}


// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func jsonErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
}

func jsonBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20)) // 32 MB cap
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// isTimeout returns true if err is a network timeout (e.g. from SetReadDeadline).
func isTimeout(err error) bool {
	type timeouter interface{ Timeout() bool }
	if te, ok := err.(timeouter); ok {
		return te.Timeout()
	}
	return false
}

// limitMiddleware wraps a handler with the per-IP rate limiter.
func (s *Server) limitMiddleware(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil && !s.limiter.allow(extractIP(r)) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	})
}

// extractIP strips the port from r.RemoteAddr.
func extractIP(r *http.Request) string {
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i != -1 {
		ip = ip[:i]
	}
	return ip
}
