// Package crypto provides the cryptographic primitives used by the interact
// system:
//
//   - AES-256-GCM for authenticated encryption of Payload data at rest and in
//     transit.
//   - HMAC-SHA256 for integrity-protecting individual binary wire frames.
//   - PBKDF2-SHA256 for deriving per-session keys from the shared secret token.
//
// None of the functions in this package call into CGo or platform-specific
// libraries; they rely only on the Go standard library and golang.org/x/crypto.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// ─────────────────────────────────────────────────────────────────────────────
// Key derivation
// ─────────────────────────────────────────────────────────────────────────────

const (
	pbkdf2Iterations = 100_000
	keyLen           = 32 // AES-256
	SaltLen          = 16
)

// DeriveKey stretches password with PBKDF2-SHA256 using salt.
// salt must be SaltLen bytes; use NewSalt() to generate one.
func DeriveKey(password, salt []byte) []byte {
	return pbkdf2.Key(password, salt, pbkdf2Iterations, keyLen, sha256.New)
}

// NewSalt returns a cryptographically random SaltLen-byte value.
func NewSalt() ([]byte, error) {
	b := make([]byte, SaltLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AES-256-GCM payload encryption
// ─────────────────────────────────────────────────────────────────────────────

// Encrypt encrypts plaintext with AES-256-GCM using key.
// The returned ciphertext is: nonce(12) || sealed-data.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts AES-256-GCM ciphertext produced by Encrypt.
func Decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// HMAC-SHA256
// ─────────────────────────────────────────────────────────────────────────────

// Sign returns a 32-byte HMAC-SHA256 of data with key.
func Sign(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// Verify reports whether mac is the correct HMAC-SHA256 of data with key.
// Uses hmac.Equal for constant-time comparison.
func Verify(key, data, mac []byte) bool {
	expected := Sign(key, data)
	return hmac.Equal(expected, mac)
}

// ─────────────────────────────────────────────────────────────────────────────
// Authenticated wire frames
// ─────────────────────────────────────────────────────────────────────────────

// AuthFrameLen is the size of a signed wire frame: 13-byte body + 32-byte MAC.
const AuthFrameLen = events.WireLen + 32

// SignFrame appends a 32-byte HMAC-SHA256 to the 13-byte wire frame,
// producing a 45-byte authenticated frame.
func SignFrame(key []byte, frame [events.WireLen]byte) [AuthFrameLen]byte {
	var out [AuthFrameLen]byte
	copy(out[:events.WireLen], frame[:])
	mac := Sign(key, frame[:])
	copy(out[events.WireLen:], mac)
	return out
}

// VerifyFrame checks the MAC of a 45-byte authenticated frame and returns the
// inner 13-byte frame on success.
func VerifyFrame(key []byte, af [AuthFrameLen]byte) ([events.WireLen]byte, bool) {
	var frame [events.WireLen]byte
	copy(frame[:], af[:events.WireLen])
	mac := af[events.WireLen:]
	return frame, Verify(key, frame[:], mac)
}

// ─────────────────────────────────────────────────────────────────────────────
// Challenge-response types and helpers
// ─────────────────────────────────────────────────────────────────────────────

// NonceLen is the size of a challenge nonce in bytes.
const NonceLen = 32

// MaxChallengeAge is the window in which a challenge must be answered before
// it is rejected as stale (replay protection).
const MaxChallengeAge = 30 * time.Second

// Challenge is the server-side representation of an outstanding auth challenge.
type Challenge struct {
	Nonce     []byte // NonceLen random bytes
	Salt      []byte // SaltLen bytes for PBKDF2 key derivation
	IssuedAt  time.Time
}

// NewChallenge generates a fresh challenge with a random nonce and salt.
func NewChallenge() (Challenge, error) {
	nonce := make([]byte, NonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Challenge{}, err
	}
	salt, err := NewSalt()
	if err != nil {
		return Challenge{}, err
	}
	return Challenge{Nonce: nonce, Salt: salt, IssuedAt: time.Now()}, nil
}

// Solve computes the HMAC-SHA256 response to a challenge using the shared
// token as the password.
func Solve(token string, c Challenge) []byte {
	key := DeriveKey([]byte(token), c.Salt)
	return Sign(key, c.Nonce)
}

// Verify checks that response is a valid answer to c using token.
// Also enforces the MaxChallengeAge freshness window.
func (c Challenge) Verify(token string, response []byte) bool {
	if time.Since(c.IssuedAt) > MaxChallengeAge {
		return false
	}
	expected := Solve(token, c)
	return hmac.Equal(expected, response)
}

// ─────────────────────────────────────────────────────────────────────────────
// Payload envelope helpers
// ─────────────────────────────────────────────────────────────────────────────

// EncryptPayload encrypts p.Data in-place using the derived key and stores the
// salt in p.Metadata["enc_salt"].  The plaintext is replaced with ciphertext.
func EncryptPayload(p *events.Payload, token string) error {
	salt, err := NewSalt()
	if err != nil {
		return err
	}
	key := DeriveKey([]byte(token), salt)
	ct, err := Encrypt(key, p.Data)
	if err != nil {
		return err
	}
	if p.Metadata == nil {
		p.Metadata = make(map[string]string)
	}
	p.Metadata["enc_salt"] = encodeHex(salt)
	p.Data = ct
	return nil
}

// DecryptPayload decrypts p.Data in-place if it was encrypted by EncryptPayload.
func DecryptPayload(p *events.Payload, token string) error {
	saltHex, ok := p.Metadata["enc_salt"]
	if !ok {
		return nil // not encrypted
	}
	salt, err := decodeHex(saltHex)
	if err != nil {
		return err
	}
	key := DeriveKey([]byte(token), salt)
	plain, err := Decrypt(key, p.Data)
	if err != nil {
		return fmt.Errorf("decrypt payload: %w", err)
	}
	p.Data = plain
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Signed WireEvent serialisation helpers
// ─────────────────────────────────────────────────────────────────────────────

// PackSigned packs a WireEvent into a 45-byte authenticated frame using key.
func PackSigned(key []byte, ev events.WireEvent) [AuthFrameLen]byte {
	frame := events.Pack(ev)
	return SignFrame(key, frame)
}

// UnpackSigned verifies and unpacks a 45-byte slice.
// Returns the event and true on success, or zero value + false on MAC failure.
func UnpackSigned(key, data []byte) (events.WireEvent, bool) {
	if len(data) < AuthFrameLen {
		return events.WireEvent{}, false
	}
	var af [AuthFrameLen]byte
	copy(af[:], data[:AuthFrameLen])
	frame, ok := VerifyFrame(key, af)
	if !ok {
		return events.WireEvent{}, false
	}
	return events.Unpack(frame), true
}

// ─────────────────────────────────────────────────────────────────────────────
// Wire serialisation for Challenge / Response (used by server auth.go)
// ─────────────────────────────────────────────────────────────────────────────

// MarshalChallenge serialises a Challenge as:
//   nonce(32) || salt(16) || issued_unix_sec(8, big-endian)
func MarshalChallenge(c Challenge) []byte {
	b := make([]byte, NonceLen+SaltLen+8)
	copy(b[:NonceLen], c.Nonce)
	copy(b[NonceLen:NonceLen+SaltLen], c.Salt)
	binary.BigEndian.PutUint64(b[NonceLen+SaltLen:], uint64(c.IssuedAt.Unix()))
	return b
}

// UnmarshalChallenge deserialises a Challenge from the binary format above.
func UnmarshalChallenge(b []byte) (Challenge, error) {
	want := NonceLen + SaltLen + 8
	if len(b) < want {
		return Challenge{}, fmt.Errorf("challenge too short: %d < %d", len(b), want)
	}
	c := Challenge{
		Nonce:    make([]byte, NonceLen),
		Salt:     make([]byte, SaltLen),
		IssuedAt: time.Unix(int64(binary.BigEndian.Uint64(b[NonceLen+SaltLen:])), 0),
	}
	copy(c.Nonce, b[:NonceLen])
	copy(c.Salt, b[NonceLen:NonceLen+SaltLen])
	return c, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Hex helpers (avoid importing encoding/hex in every caller)
// ─────────────────────────────────────────────────────────────────────────────

const hextable = "0123456789abcdef"

func encodeHex(src []byte) string {
	dst := make([]byte, len(src)*2)
	for i, v := range src {
		dst[i*2] = hextable[v>>4]
		dst[i*2+1] = hextable[v&0x0f]
	}
	return string(dst)
}

func decodeHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("odd hex length")
	}
	dst := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi := hexVal(s[i])
		lo := hexVal(s[i+1])
		if hi == 255 || lo == 255 {
			return nil, fmt.Errorf("invalid hex char at %d", i)
		}
		dst[i/2] = hi<<4 | lo
	}
	return dst, nil
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 255
}

