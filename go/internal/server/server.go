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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/newuser-admin/claude/interact/internal/jitter"
	isync "github.com/newuser-admin/claude/interact/internal/sync"
	"github.com/newuser-admin/claude/interact/internal/store"
	shellpty "github.com/newuser-admin/claude/interact/internal/pty"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// Config holds server-wide settings.
type Config struct {
	Token      string        // shared secret for auth
	JitterDelay time.Duration // jitter buffer window for /ws/interact (default 100ms)
	NodeID     string        // this server's sync identity
}

// Server is the main HTTP + WebSocket handler.
type Server struct {
	cfg      Config
	store    *store.Store
	sync     *isync.Engine
	upgrader websocket.Upgrader
	mux      *http.ServeMux
}

// New wires up all handlers and returns a ready Server.
func New(cfg Config, st *store.Store) *Server {
	if cfg.JitterDelay == 0 {
		cfg.JitterDelay = 100 * time.Millisecond
	}
	s := &Server{
		cfg:   cfg,
		store: st,
		sync:  isync.New(cfg.NodeID, st),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/api/interactions", s.auth(s.handleInteractions))
	s.mux.HandleFunc("/api/interactions/", s.auth(s.handleInteraction))
	s.mux.HandleFunc("/api/payloads", s.auth(s.handlePayloads))
	s.mux.HandleFunc("/api/payloads/", s.auth(s.handlePayload))
	s.mux.HandleFunc("/ws/interact", s.auth(s.handleWSInteract))
	s.mux.HandleFunc("/ws/shell", s.auth(s.handleWSShell))
	s.mux.HandleFunc("/ws/sync", s.auth(s.handleWSSync))
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "node": cfg.NodeID})
	})
	return s
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
		if err := s.store.SavePayload(&p); err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
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
		jsonOK(w, p)
		return
	}

	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// ─────────────────────────────────────────────────────────────────────────────
// WebSocket: /ws/interact  (binary — WireEvent recording / playback)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleWSInteract(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	remote := r.RemoteAddr
	fmt.Printf("[interact] client connected: %s\n", remote)
	defer fmt.Printf("[interact] client disconnected: %s\n", remote)

	// mode: "record" stores incoming binary frames; "play" streams an interaction
	mode := r.URL.Query().Get("mode")
	name := r.URL.Query().Get("name")
	id   := r.URL.Query().Get("id")

	switch mode {
	case "play":
		s.wsPlayback(ws, id)
	default: // "record" or empty → record mode
		s.wsRecord(ws, name)
	}
}

// wsRecord receives binary WireEvent frames from the client, buffers them
// through the jitter filter, and persists a named Interaction on close.
func (s *Server) wsRecord(ws *websocket.Conn, name string) {
	if name == "" {
		name = fmt.Sprintf("recording-%s", time.Now().Format("20060102-150405"))
	}

	jb := jitter.New(s.cfg.JitterDelay)
	var captured []events.WireEvent

	stopJitter := jb.Run(5*time.Millisecond, func(ev events.WireEvent) {
		captured = append(captured, ev)
	})
	defer stopJitter()

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		ev, ok := events.UnpackSlice(data)
		if !ok {
			continue
		}
		jb.Push(ev)
	}

	// Drain any remaining buffered events.
	time.Sleep(s.cfg.JitterDelay + 10*time.Millisecond)
	jb.Tick(func(ev events.WireEvent) { captured = append(captured, ev) })
	stopJitter()

	if len(captured) == 0 {
		return
	}

	ia := &events.Interaction{
		Name:   name,
		Events: captured,
	}
	if err := s.store.SaveInteraction(ia); err != nil {
		fmt.Printf("[interact] save error: %v\n", err)
		return
	}
	fmt.Printf("[interact] saved %q (%d events)\n", ia.Name, len(ia.Events))
	s.sync.Tick()
}

// wsPlayback loads an Interaction by id and streams its WireEvents as binary
// frames at their original relative timing.
func (s *Server) wsPlayback(ws *websocket.Conn, id string) {
	ia, err := s.store.GetInteraction(id)
	if err != nil {
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(4004, "interaction not found"))
		return
	}

	start := time.Now()
	for _, ev := range ia.Events {
		target := start.Add(time.Duration(ev.T) * time.Millisecond)
		if wait := time.Until(target); wait > 0 {
			time.Sleep(wait)
		}
		b := events.Pack(ev)
		if err := ws.WriteMessage(websocket.BinaryMessage, b[:]); err != nil {
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WebSocket: /ws/shell  (JSON PTY)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleWSShell(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	fmt.Printf("[shell] client connected: %s\n", r.RemoteAddr)
	shellpty.Handler(ws, r.RemoteAddr)
}

// ─────────────────────────────────────────────────────────────────────────────
// WebSocket: /ws/sync  (JSON bidirectional sync)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleWSSync(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

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
