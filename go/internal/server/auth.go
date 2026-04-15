package server

// auth.go — WebSocket challenge-response authentication.
//
// When --auth-challenge is enabled (the default for production) every WebSocket
// upgrade goes through a two-frame handshake BEFORE any application data:
//
//   Server → Client  (binary, 56 bytes)
//     challenge_frame = nonce(32) || salt(16) || issued_unix_sec(8, big-endian)
//
//   Client → Server  (binary, 32 bytes)
//     response_frame = HMAC-SHA256( PBKDF2(token, salt, 100k, 32, SHA256), nonce )
//
// If verification fails the server closes the connection with code 4001.
// This avoids the token ever appearing in URLs or log files and adds
// replay-protection via the freshness window enforced by crypto.Challenge.Verify.
//
// Legacy mode (--auth-challenge=false) falls back to the ?token= query param /
// Authorization header checked in server.go, so shell-client.html keeps working
// without modification.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	icrypto "github.com/newuser-admin/claude/interact/internal/crypto"
)

// challengeHandshake performs the binary challenge-response handshake on ws.
// Returns true if the client authenticated successfully.
func (s *Server) challengeHandshake(ws *websocket.Conn) bool {
	ch, err := icrypto.NewChallenge()
	if err != nil {
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(1011, "internal error"))
		return false
	}

	// Send challenge as JSON so shell-client.html can optionally parse it.
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
		return false
	}

	// Receive response.
	_, raw, err := ws.ReadMessage()
	if err != nil {
		return false
	}

	// Accept both JSON {"type":"auth","response":"<hex>"} and raw 32-byte binary.
	var response []byte
	if len(raw) == 32 {
		response = raw
	} else {
		type authMsg struct {
			Type     string `json:"type"`
			Response string `json:"response"` // hex
		}
		var am authMsg
		if json.Unmarshal(raw, &am) == nil && am.Type == "auth" {
			response, _ = hex.DecodeString(am.Response)
		}
	}

	if !ch.Verify(s.cfg.Token, response) {
		ws.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(4001, "Unauthorized"))
		return false
	}
	return true
}

// wsAuth is an alternative to the simple token middleware for WebSocket
// endpoints. It uses challenge-response when cfg.ChallengeAuth is true,
// otherwise falls back to token-in-URL / header.
func (s *Server) wsAuth(w http.ResponseWriter, r *http.Request, handler func(*websocket.Conn)) {
	if !s.cfg.ChallengeAuth {
		// Legacy: token checked before upgrade.
		if tokenFromRequest(r) != s.cfg.Token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		handler(ws)
		return
	}

	// Challenge-response: upgrade first, then authenticate.
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	if !s.challengeHandshake(ws) {
		fmt.Printf("[auth] challenge failed from %s\n", r.RemoteAddr)
		return
	}
	handler(ws)
}
