package server

// auth.go — WebSocket challenge-response authentication.
//
// When --auth-challenge is enabled every WebSocket upgrade goes through a
// two-message handshake BEFORE any application data:
//
//   Server → Client  (text JSON)
//     {"type":"challenge","nonce":"<hex32>","salt":"<hex16>"}
//
//   Client → Server  (text JSON or raw 32-byte binary)
//     {"type":"auth","response":"<hex32>"}
//     where response = HMAC-SHA256(PBKDF2(token, salt, 100k, 32, SHA256), nonce)
//
// On success the server sends {"type":"ok"} and both sides use
// PBKDF2(token, salt) as the per-session signing key for --sign-frames.
// On failure the server closes with code 4001.
//
// When --sign-frames is enabled WITHOUT --auth-challenge a lighter-weight
// salt-only exchange runs:
//
//   Server → Client  (text JSON)
//     {"type":"session","salt":"<hex16>"}
//
// No token verification is performed in this branch (token already checked
// by the pre-upgrade HTTP middleware), but the salt still makes the signing
// key unique per connection.
//
// Legacy mode (neither flag) falls back to the ?token= / Authorization header
// checked in server.go — shell-client.html continues to work unmodified.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	icrypto "github.com/newuser-admin/claude/interact/internal/crypto"
)

// challengeHandshake performs the full challenge-response handshake on ws.
// It returns the derived per-session signing key and true on success,
// or nil and false if the client fails to authenticate.
func (s *Server) challengeHandshake(ws *websocket.Conn) ([]byte, bool) {
	ch, err := icrypto.NewChallenge()
	if err != nil {
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(1011, "internal error"))
		return nil, false
	}

	// Send challenge.
	type challengeMsg struct {
		Type  string `json:"type"`
		Nonce string `json:"nonce"` // hex
		Salt  string `json:"salt"`  // hex
	}
	cm := challengeMsg{
		Type:  "challenge",
		Nonce: hex.EncodeToString(ch.Nonce),
		Salt:  hex.EncodeToString(ch.Salt),
	}
	data, _ := json.Marshal(cm)
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		return nil, false
	}

	// Receive response — accept JSON or raw 32-byte binary.
	_, raw, err := ws.ReadMessage()
	if err != nil {
		return nil, false
	}
	var response []byte
	if len(raw) == icrypto.NonceLen {
		response = raw
	} else {
		var am struct {
			Type     string `json:"type"`
			Response string `json:"response"`
		}
		if json.Unmarshal(raw, &am) == nil && am.Type == "auth" {
			response, _ = hex.DecodeString(am.Response)
		}
	}

	if !ch.Verify(s.cfg.Token, response) {
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(4001, "Unauthorized"))
		return nil, false
	}

	// Confirm success and return the session key derived from challenge's salt.
	ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"ok"}`)) //nolint:errcheck
	key := icrypto.DeriveKey([]byte(s.cfg.Token), ch.Salt)
	return key, true
}

// saltExchange sends a random salt and derives a session signing key from it.
// Used when --sign-frames is on but --auth-challenge is off; the token is
// already validated by the HTTP middleware before the WS upgrade.
func (s *Server) saltExchange(ws *websocket.Conn) ([]byte, bool) {
	salt, err := icrypto.NewSalt()
	if err != nil {
		return nil, false
	}
	msg, _ := json.Marshal(map[string]string{
		"type": "session",
		"salt": hex.EncodeToString(salt),
	})
	if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
		return nil, false
	}
	return icrypto.DeriveKey([]byte(s.cfg.Token), salt), true
}

// wsAuth upgrades the connection, authenticates it, and calls handler with
// the WebSocket and the per-session signing key (nil when signing is off).
// It also increments the server's WaitGroup so Shutdown can drain cleanly.
func (s *Server) wsAuth(w http.ResponseWriter, r *http.Request, handler func(*websocket.Conn, []byte)) {
	s.wsWg.Add(1)
	defer s.wsWg.Done()
	if !s.cfg.ChallengeAuth {
		// Legacy HTTP-level token check.
		if tokenFromRequest(r) != s.cfg.Token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()

		var sessionKey []byte
		if s.cfg.SignFrames {
			var ok bool
			sessionKey, ok = s.saltExchange(ws)
			if !ok {
				return
			}
		}
		handler(ws, sessionKey)
		return
	}

	// Challenge-response: upgrade first, then authenticate.
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	key, ok := s.challengeHandshake(ws)
	if !ok {
		fmt.Printf("[auth] challenge failed from %s\n", r.RemoteAddr)
		return
	}
	handler(ws, key)
}
