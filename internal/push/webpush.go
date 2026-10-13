// Package push notifies the user's phones when a session needs them. A phone
// (the Android app, through a UnifiedPush distributor such as ntfy) registers a
// WebPush subscription; the notifier watches the running sessions' journals and
// posts an end-to-end encrypted message to each subscription.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxBody is the largest request body a push service must accept (RFC 8030 §7.2).
const maxBody = 4096

// recordOverhead is what encryption adds to the plaintext: the aes128gcm header
// (salt 16 + record size 4 + key id length 1 + sender key 65), the padding
// delimiter and the AEAD tag.
const recordOverhead = 16 + 4 + 1 + 65 + 1 + 16

// ErrGone reports a subscription the push service no longer knows: forget it.
var ErrGone = errors.New("push subscription is gone")

// decodeKey accepts the base64url form WebPush uses, padded or not.
func decodeKey(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// Encrypt seals plaintext for one subscription (RFC 8291, aes128gcm): only the
// phone holding the private half of p256dh can read it, the relay cannot.
func Encrypt(plaintext []byte, p256dh, auth string) ([]byte, error) {
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(plaintext, p256dh, auth, sender, salt)
}

func encrypt(plaintext []byte, p256dh, auth string, sender *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext)+recordOverhead > maxBody {
		return nil, fmt.Errorf("push payload too large (%d bytes)", len(plaintext))
	}
	uaPublic, err := decodeKey(p256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := decodeKey(auth)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	receiver, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	shared, err := sender.ECDH(receiver)
	if err != nil {
		return nil, err
	}
	asPublic := sender.PublicKey().Bytes()

	keyInfo := append([]byte("WebPush: info\x00"), uaPublic...)
	keyInfo = append(keyInfo, asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(plaintext)+recordOverhead)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, maxBody)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	// A single record: the plaintext followed by the last-record delimiter.
	return gcm.Seal(out, nonce, append(bytes.Clone(plaintext), 0x02), nil), nil
}

// Send encrypts payload and posts it to the subscription's endpoint. ErrGone
// means the endpoint is dead and must be forgotten.
func Send(ctx context.Context, client *http.Client, endpoint, p256dh, auth string, payload []byte) error {
	body, err := Encrypt(payload, p256dh, auth)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "high")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode >= 300:
		return fmt.Errorf("push service answered %s", res.Status)
	}
	return nil
}
