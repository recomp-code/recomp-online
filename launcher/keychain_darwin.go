package main

// protect / unprotect on macOS: the saved login token is encrypted (AES-256-GCM) with a key kept in the user's
// Keychain, so online.json alone is useless on another Mac or account. The key goes through /usr/bin/security: its
// Keychain item then trusts that Apple-signed tool, not this ad-hoc signed app, so an update of the launcher reads it
// without a password prompt.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
)

const keychainService, keychainAccount = "Recomp launcher", "session-key"

func keychainKey(create bool) ([]byte, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", keychainService, "-a", keychainAccount, "-w").Output()
	if err == nil {
		if k, err := hex.DecodeString(strings.TrimSpace(string(out))); err == nil && len(k) == 32 {
			return k, nil
		}
	}
	if !create {
		return nil, errors.New("no key in the Keychain")
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", keychainService, "-a", keychainAccount,
		"-w", hex.EncodeToString(k)).Run(); err != nil {
		return nil, err
	}
	return k, nil
}

func sealer(create bool) (cipher.AEAD, error) {
	k, err := keychainKey(create)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func protect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	g, err := sealer(true)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, b, nil), nil
}

func unprotect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	g, err := sealer(false)
	if err != nil {
		return nil, err
	}
	if len(b) < g.NonceSize() {
		return nil, errors.New("short")
	}
	return g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], nil)
}
