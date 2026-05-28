package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	saltLen    = 16
	nonceLen   = 12
	gcmTagLen  = 16
	keyLen     = 32 // AES-256
	pbkdf2Iter = 100_000
	minBlobLen = saltLen + nonceLen + gcmTagLen
)

// Encrypt encrypts plaintext with AES-256-GCM using a key derived from
// passphrase via PBKDF2-SHA256. Each call produces a unique ciphertext.
// Returns base64(salt[16] + nonce[12] + ciphertext).
func Encrypt(plaintext, passphrase string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("crypto: salt: %w", err)
	}

	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}

	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	blob := make([]byte, 0, saltLen+nonceLen+len(ct))
	blob = append(blob, salt...)
	blob = append(blob, nonce...)
	blob = append(blob, ct...)
	return base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt decrypts a base64-encoded blob produced by Encrypt.
// Returns an error if the passphrase is wrong or the data is tampered.
func Decrypt(encoded, passphrase string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("crypto: base64: %w", err)
	}
	if len(blob) < minBlobLen {
		return "", errors.New("crypto: blob too short")
	}

	salt := blob[:saltLen]
	nonce := blob[saltLen : saltLen+nonceLen]
	ct := blob[saltLen+nonceLen:]

	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return "", err
	}

	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", errors.New("crypto: decrypt failed (wrong passphrase or corrupted data)")
	}
	return string(pt), nil
}

func newGCM(passphrase string, salt []byte) (cipher.AEAD, error) {
	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, keyLen, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	return gcm, nil
}
