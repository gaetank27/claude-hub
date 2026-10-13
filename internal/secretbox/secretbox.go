// Package secretbox provides authenticated symmetric encryption (AES-256-GCM)
// for secrets stored at rest — connector credentials (API tokens, passwords, …).
// The key lives outside the database (a 0600 key file, or the SYSTEM_SECRET_KEY
// env var) so a database dump alone never leaks plaintext secrets.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keyLen = 32 // AES-256

// Box encrypts and decrypts short secrets with a single AES-256-GCM key.
type Box struct {
	gcm cipher.AEAD
}

// Open returns a Box keyed by SYSTEM_SECRET_KEY (base64, 32 bytes) when set,
// otherwise by the key file at keyPath — generated with crypto/rand on first run
// and persisted with 0600 perms so it survives restarts.
func Open(keyPath string) (*Box, error) {
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{gcm: gcm}, nil
}

// Encrypt seals plaintext and returns base64(nonce || ciphertext || tag).
func (b *Box) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := b.gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt, authenticating the ciphertext.
func (b *Box) Decrypt(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	ns := b.gcm.NonceSize()
	if len(raw) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	return b.gcm.Open(nil, nonce, ct, nil)
}

func loadOrCreateKey(keyPath string) ([]byte, error) {
	if env := os.Getenv("SYSTEM_SECRET_KEY"); env != "" {
		key, err := base64.StdEncoding.DecodeString(env)
		if err != nil {
			return nil, fmt.Errorf("SYSTEM_SECRET_KEY: invalid base64: %w", err)
		}
		if len(key) != keyLen {
			return nil, fmt.Errorf("SYSTEM_SECRET_KEY: want %d bytes, got %d", keyLen, len(key))
		}
		return key, nil
	}
	if b, err := os.ReadFile(keyPath); err == nil {
		if len(b) != keyLen {
			return nil, fmt.Errorf("key file %s: want %d bytes, got %d", keyPath, keyLen, len(b))
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// First run: mint and persist a fresh key.
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return key, nil
}
