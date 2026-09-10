// Package ledger — at-rest encryption helpers for F-01.
//
// The default build stays pure-Go (modernc.org/sqlite, no CGO). When the
// operator sets KANZU_DB_KEY, the DB file can be wrapped with AES-256-GCM
// so the bytes on disk are opaque. The live file remains 0600 either way.
//
// Design:
//   - Header: 10 bytes "KANZU_ENC\x01"
//   - Salt: 16 random bytes (for PBKDF2)
//   - Nonce: 12 random bytes (for GCM)
//   - Ciphertext: AES-256-GCM(key, nonce, plaintext)
//   - Key: PBKDF2-HMAC-SHA256(passphrase, salt, 100_000 iterations, 32 bytes)
//   - Decrypt validates header + GCM tag; wrong passphrase fails closed.
//
// This is deliberately not SQLCipher — it keeps the zero-CGO invariant and
// costs ~80 lines of stdlib. An operator who needs SQLCipher can still
// build with the `sqlcipher` tag (see ledger_sqlcipher.go) for page-level
// encryption; this file covers the file-level path that works today.
package ledger

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
)

var encHeader = []byte("KANZU_ENC\x01") // 10 bytes

// IsEncrypted reports whether path starts with the KANZU_ENC header.
func IsEncrypted(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	hdr := make([]byte, len(encHeader))
	if _, err := io.ReadFull(f, hdr); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(hdr, encHeader), nil
}

// EncryptFile reads plaintext at srcPath and writes an encrypted envelope to
// dstPath (0600, atomic via .tmp + rename). Passphrase must be non-empty.
func EncryptFile(srcPath, dstPath, passphrase string) error {
	if passphrase == "" {
		return errors.New("encrypt: empty passphrase")
	}
	plain, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("encrypt read: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("encrypt salt: %w", err)
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("encrypt nonce: %w", err)
	}
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("encrypt cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("encrypt gcm: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	// zero key material
	for i := range key {
		key[i] = 0
	}
	tmp := dstPath + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("encrypt open dst: %w", err)
	}
	if _, err := out.Write(encHeader); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := out.Write(salt); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := out.Write(nonce); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := out.Write(ct); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// DecryptFile reads an encrypted envelope at srcPath and writes plaintext to
// dstPath (0600, atomic). Wrong passphrase or tampered file fails closed.
func DecryptFile(srcPath, dstPath, passphrase string) error {
	if passphrase == "" {
		return errors.New("decrypt: empty passphrase")
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("decrypt open: %w", err)
	}
	defer f.Close()
	hdr := make([]byte, len(encHeader))
	if _, err := io.ReadFull(f, hdr); err != nil {
		return fmt.Errorf("decrypt header: %w", err)
	}
	if !bytes.Equal(hdr, encHeader) {
		return errors.New("decrypt: not a KANZU_ENC file")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(f, salt); err != nil {
		return fmt.Errorf("decrypt salt: %w", err)
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(f, nonce); err != nil {
		return fmt.Errorf("decrypt nonce: %w", err)
	}
	ct, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("decrypt body: %w", err)
	}
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("decrypt cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("decrypt gcm: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return fmt.Errorf("decrypt: wrong passphrase or tampered file: %w", err)
	}
	tmp := dstPath + ".tmp"
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		return fmt.Errorf("decrypt write: %w", err)
	}
	return os.Rename(tmp, dstPath)
}

func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	// PBKDF2-HMAC-SHA256 with 100k iterations — pure stdlib, no x/crypto.
	// Strong enough for a laptop passphrase, cheap enough for init.
	// Returns 32 bytes for AES-256.
	return pbkdf2HMACSHA256([]byte(passphrase), salt, 100_000, 32), nil
}

// pbkdf2HMACSHA256 is a minimal PBKDF2 implementation using only stdlib.
func pbkdf2HMACSHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	dk := make([]byte, 0, numBlocks*hLen)
	var blockNum uint32 = 1
	for i := 0; i < numBlocks; i++ {
		mac := hmac.New(func() hash.Hash { return sha256.New() }, password)
		mac.Write(salt)
		mac.Write([]byte{byte(blockNum >> 24), byte(blockNum >> 16), byte(blockNum >> 8), byte(blockNum)})
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for j := 1; j < iter; j++ {
			mac = hmac.New(func() hash.Hash { return sha256.New() }, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		dk = append(dk, t...)
		blockNum++
	}
	return dk[:keyLen]
}
