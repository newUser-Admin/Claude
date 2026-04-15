package crypto_test

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/newuser-admin/claude/interact/internal/crypto"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

// ─── Key derivation ───────────────────────────────────────────────────────────

func TestDeriveKey_length(t *testing.T) {
	key := crypto.DeriveKey([]byte("password"), []byte("saltsaltsaltsalt"))
	if len(key) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key))
	}
}

func TestDeriveKey_deterministic(t *testing.T) {
	pwd := []byte("s3cr3t")
	salt := []byte("abcdefghijklmnop") // 16 bytes
	k1 := crypto.DeriveKey(pwd, salt)
	k2 := crypto.DeriveKey(pwd, salt)
	if !bytes.Equal(k1, k2) {
		t.Fatal("DeriveKey is not deterministic")
	}
}

func TestDeriveKey_differentSalts(t *testing.T) {
	pwd := []byte("same-password")
	k1 := crypto.DeriveKey(pwd, bytes.Repeat([]byte{0x01}, 16))
	k2 := crypto.DeriveKey(pwd, bytes.Repeat([]byte{0x02}, 16))
	if bytes.Equal(k1, k2) {
		t.Fatal("different salts produced identical keys")
	}
}

func TestNewSalt(t *testing.T) {
	s1, err := crypto.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := crypto.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	if len(s1) != crypto.SaltLen {
		t.Fatalf("wrong salt length: %d", len(s1))
	}
	if bytes.Equal(s1, s2) {
		t.Fatal("NewSalt returned identical values — PRNG broken")
	}
}

// ─── AES-256-GCM ─────────────────────────────────────────────────────────────

func TestEncryptDecrypt_roundtrip(t *testing.T) {
	key := crypto.DeriveKey([]byte("key"), bytes.Repeat([]byte{0xAA}, 16))
	plain := []byte("hello, interact!")

	ct, err := crypto.Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, plain) {
		t.Fatal("ciphertext equals plaintext")
	}

	got, err := crypto.Decrypt(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", got, plain)
	}
}

func TestEncrypt_uniqueNonces(t *testing.T) {
	key := crypto.DeriveKey([]byte("key"), bytes.Repeat([]byte{0x11}, 16))
	plain := []byte("same plaintext")

	ct1, _ := crypto.Encrypt(key, plain)
	ct2, _ := crypto.Encrypt(key, plain)
	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext — nonce reuse!")
	}
}

func TestDecrypt_wrongKey(t *testing.T) {
	key1 := crypto.DeriveKey([]byte("key1"), bytes.Repeat([]byte{0x22}, 16))
	key2 := crypto.DeriveKey([]byte("key2"), bytes.Repeat([]byte{0x22}, 16))

	ct, _ := crypto.Encrypt(key1, []byte("secret"))
	_, err := crypto.Decrypt(key2, ct)
	if err == nil {
		t.Fatal("decryption with wrong key should fail")
	}
}

func TestDecrypt_tamperedCiphertext(t *testing.T) {
	key := crypto.DeriveKey([]byte("k"), bytes.Repeat([]byte{0x33}, 16))
	ct, _ := crypto.Encrypt(key, []byte("data"))
	ct[len(ct)-1] ^= 0xFF // flip last byte

	_, err := crypto.Decrypt(key, ct)
	if err == nil {
		t.Fatal("tampered ciphertext should fail authentication")
	}
}

func TestDecrypt_tooShort(t *testing.T) {
	key := crypto.DeriveKey([]byte("k"), bytes.Repeat([]byte{0x44}, 16))
	_, err := crypto.Decrypt(key, []byte{1, 2, 3})
	if err == nil {
		t.Fatal("short ciphertext should fail")
	}
}

// ─── HMAC-SHA256 ─────────────────────────────────────────────────────────────

func TestSignVerify_valid(t *testing.T) {
	key := []byte("hmac-key-32-bytes-padded-to-size")
	data := []byte("authenticate me")
	mac := crypto.Sign(key, data)
	if len(mac) != 32 {
		t.Fatalf("expected 32-byte MAC, got %d", len(mac))
	}
	if !crypto.Verify(key, data, mac) {
		t.Fatal("Verify rejected a valid MAC")
	}
}

func TestVerify_wrongKey(t *testing.T) {
	key1 := []byte("key-one-32-bytes-exactly-padded!")
	key2 := []byte("key-two-32-bytes-exactly-padded!")
	mac := crypto.Sign(key1, []byte("data"))
	if crypto.Verify(key2, []byte("data"), mac) {
		t.Fatal("Verify accepted MAC with wrong key")
	}
}

func TestVerify_tamperedData(t *testing.T) {
	key := []byte("stable-key-32-bytes-padded-here!")
	mac := crypto.Sign(key, []byte("original"))
	if crypto.Verify(key, []byte("tampered"), mac) {
		t.Fatal("Verify accepted MAC for different data")
	}
}

// ─── Authenticated wire frames ────────────────────────────────────────────────

func TestSignVerifyFrame_roundtrip(t *testing.T) {
	key := crypto.DeriveKey([]byte("frame-key"), bytes.Repeat([]byte{0x55}, 16))

	ev := events.WireEvent{Type: events.MsgKey, T: 1234, V1: float32('a'), V2: 0}
	frame := events.Pack(ev)

	signed := crypto.SignFrame(key, frame)
	if len(signed) != crypto.AuthFrameLen {
		t.Fatalf("signed frame wrong length: %d", len(signed))
	}

	got, ok := crypto.VerifyFrame(key, signed)
	if !ok {
		t.Fatal("VerifyFrame rejected a valid frame")
	}
	if got != frame {
		t.Fatal("frame mismatch after verify")
	}
}

func TestVerifyFrame_tampered(t *testing.T) {
	key := crypto.DeriveKey([]byte("fk"), bytes.Repeat([]byte{0x66}, 16))
	frame := events.Pack(events.WireEvent{Type: events.MsgScroll, T: 99, V1: 1, V2: -1})
	signed := crypto.SignFrame(key, frame)
	signed[0] ^= 0xFF // tamper with type byte

	_, ok := crypto.VerifyFrame(key, signed)
	if ok {
		t.Fatal("VerifyFrame accepted tampered frame")
	}
}

func TestPackSignedUnpackSigned_roundtrip(t *testing.T) {
	key := crypto.DeriveKey([]byte("pack-key"), bytes.Repeat([]byte{0x77}, 16))
	ev := events.WireEvent{Type: events.MsgClick, T: 500, V1: 0.3, V2: 0.7}

	af := crypto.PackSigned(key, ev)
	got, ok := crypto.UnpackSigned(key, af[:])
	if !ok {
		t.Fatal("UnpackSigned failed on valid frame")
	}
	if got != ev {
		t.Fatalf("event mismatch: got %+v, want %+v", got, ev)
	}
}

func TestUnpackSigned_tooShort(t *testing.T) {
	key := crypto.DeriveKey([]byte("k"), bytes.Repeat([]byte{0x88}, 16))
	_, ok := crypto.UnpackSigned(key, []byte{1, 2, 3})
	if ok {
		t.Fatal("UnpackSigned should fail on short input")
	}
}

// ─── Challenge-response ───────────────────────────────────────────────────────

func TestChallenge_validResponse(t *testing.T) {
	token := "supersecret"
	ch, err := crypto.NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Nonce) != crypto.NonceLen {
		t.Fatalf("nonce wrong length: %d", len(ch.Nonce))
	}
	if len(ch.Salt) != crypto.SaltLen {
		t.Fatalf("salt wrong length: %d", len(ch.Salt))
	}

	resp := crypto.Solve(token, ch)
	if !ch.Verify(token, resp) {
		t.Fatal("valid response was rejected")
	}
}

func TestChallenge_wrongToken(t *testing.T) {
	ch, _ := crypto.NewChallenge()
	resp := crypto.Solve("correct-token", ch)
	if ch.Verify("wrong-token", resp) {
		t.Fatal("wrong token should not verify")
	}
}

func TestChallenge_replayRejected(t *testing.T) {
	ch, _ := crypto.NewChallenge()
	// Backdate the challenge beyond the freshness window.
	ch.IssuedAt = time.Now().Add(-(crypto.MaxChallengeAge + time.Second))
	resp := crypto.Solve("tok", ch)
	if ch.Verify("tok", resp) {
		t.Fatal("stale challenge should be rejected")
	}
}

func TestChallenge_uniqueNonces(t *testing.T) {
	c1, _ := crypto.NewChallenge()
	c2, _ := crypto.NewChallenge()
	if bytes.Equal(c1.Nonce, c2.Nonce) {
		t.Fatal("two challenges shared a nonce — PRNG broken")
	}
	if bytes.Equal(c1.Salt, c2.Salt) {
		t.Fatal("two challenges shared a salt — PRNG broken")
	}
}

func TestMarshalUnmarshalChallenge(t *testing.T) {
	ch, err := crypto.NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	ch.IssuedAt = ch.IssuedAt.Truncate(time.Second) // marshal is second-precision

	b := crypto.MarshalChallenge(ch)
	got, err := crypto.UnmarshalChallenge(b)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got.Nonce, ch.Nonce) {
		t.Fatalf("nonce mismatch: %s vs %s",
			hex.EncodeToString(got.Nonce), hex.EncodeToString(ch.Nonce))
	}
	if !bytes.Equal(got.Salt, ch.Salt) {
		t.Fatal("salt mismatch")
	}
	if !got.IssuedAt.Equal(ch.IssuedAt) {
		t.Fatalf("IssuedAt mismatch: %v vs %v", got.IssuedAt, ch.IssuedAt)
	}
}

func TestUnmarshalChallenge_tooShort(t *testing.T) {
	_, err := crypto.UnmarshalChallenge([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("should fail on short input")
	}
}

// ─── Payload encryption ───────────────────────────────────────────────────────

func TestEncryptDecryptPayload(t *testing.T) {
	token := "payload-token"
	p := &events.Payload{
		ID:   "test-id",
		Name: "test",
		Data: []byte("sensitive payload data"),
	}

	plain := make([]byte, len(p.Data))
	copy(plain, p.Data)

	if err := crypto.EncryptPayload(p, token); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(p.Data, plain) {
		t.Fatal("data was not encrypted")
	}
	if p.Metadata["enc_salt"] == "" {
		t.Fatal("enc_salt metadata missing after encryption")
	}

	if err := crypto.DecryptPayload(p, token); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Data, plain) {
		t.Fatalf("decrypted data mismatch: got %q, want %q", p.Data, plain)
	}
}

func TestDecryptPayload_noopWhenUnencrypted(t *testing.T) {
	p := &events.Payload{Data: []byte("plaintext")}
	orig := append([]byte{}, p.Data...)
	if err := crypto.DecryptPayload(p, "any-token"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Data, orig) {
		t.Fatal("DecryptPayload modified an unencrypted payload")
	}
}

func TestEncryptPayload_wrongTokenFails(t *testing.T) {
	p := &events.Payload{Data: []byte("data")}
	if err := crypto.EncryptPayload(p, "correct"); err != nil {
		t.Fatal(err)
	}
	if err := crypto.DecryptPayload(p, "wrong"); err == nil {
		t.Fatal("decryption with wrong token should fail")
	}
}
