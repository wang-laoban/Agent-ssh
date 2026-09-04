package logger

import (
	"crypto"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Signer produces a signature for a canonical byte slice.
type Signer interface {
	Sign(data []byte) ([]byte, error)
	Algorithm() string
}

// LoadSigner selects and loads a signer from the configured key material.
// For Ed25519 the private key file is required; for HMAC the same file is
// treated as a shared secret (or a 32-byte random key is generated).
func LoadSigner(algo, keyFile string) (Signer, error) {
	switch algo {
	case "ed25519", "":
		return loadEd25519Signer(keyFile)
	case "hmac-sha256":
		return loadHMACSigner(keyFile)
	default:
		return nil, fmt.Errorf("unsupported signature algorithm %q", algo)
	}
}

// EnsureKeyPair guarantees that signing key material exists on disk for the
// given algorithm, generating it when the private key file is missing, so the
// agent can start directly without a prior --generate-keys step. An existing
// private key is never touched: regenerating it would break verification of
// previously signed log entries. For Ed25519 a key pair is written to
// signKeyFile and verifyPubFile (derived from the private key path when
// empty). For HMAC-SHA256 a random 32-byte secret is written to signKeyFile.
// It reports whether new key material was created.
func EnsureKeyPair(algo, signKeyFile, verifyPubFile string) (bool, error) {
	if algo == "" {
		algo = "ed25519"
	}

	if _, err := os.Stat(signKeyFile); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat signing key %q: %w", signKeyFile, err)
	}

	if dir := filepath.Dir(signKeyFile); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, fmt.Errorf("create key directory: %w", err)
		}
	}

	switch algo {
	case "ed25519":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return false, fmt.Errorf("generate ed25519 key: %w", err)
		}
		if err := writeEd25519PrivateKey(signKeyFile, priv); err != nil {
			return false, err
		}
		pubPath := verifyPubFile
		if pubPath == "" {
			pubPath = strings.TrimSuffix(signKeyFile, ".key") + ".pub"
		}
		if err := writeEd25519PublicKey(pubPath, priv.Public()); err != nil {
			return false, err
		}
	case "hmac-sha256":
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return false, fmt.Errorf("generate hmac secret: %w", err)
		}
		if err := os.WriteFile(signKeyFile, secret, 0o600); err != nil {
			return false, fmt.Errorf("write hmac key %q: %w", signKeyFile, err)
		}
	default:
		return false, fmt.Errorf("unsupported signature algorithm %q", algo)
	}
	return true, nil
}

// ed25519Signer signs with an Ed25519 private key.
type ed25519Signer struct {
	privateKey ed25519.PrivateKey
}

func loadEd25519Signer(keyFile string) (*ed25519Signer, error) {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read signing key %q: %w", keyFile, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("signing key %q is not a valid PEM block", keyFile)
	}
	if block.Type != "ED25519 PRIVATE KEY" {
		return nil, fmt.Errorf("signing key %q has unexpected PEM type %q", keyFile, block.Type)
	}
	pk, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse signing key %q: %w", keyFile, err)
	}
	edPriv, ok := pk.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key %q is not an Ed25519 private key", keyFile)
	}
	return &ed25519Signer{privateKey: edPriv}, nil
}

func (s *ed25519Signer) Sign(data []byte) ([]byte, error) {
	return s.privateKey.Sign(nil, data, crypto.Hash(0))
}

func (s *ed25519Signer) Algorithm() string { return "ed25519" }

// writeEd25519PrivateKey persists an Ed25519 private key as a PEM file.
func writeEd25519PrivateKey(path string, priv ed25519.PrivateKey) error {
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PRIVATE KEY", Bytes: pkcs8Bytes})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return fmt.Errorf("write private key %q: %w", path, err)
	}
	return nil
}

// writeEd25519PublicKey persists an Ed25519 public key as a PEM file.
func writeEd25519PublicKey(path string, pub crypto.PublicKey) error {
	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PUBLIC KEY", Bytes: pubBytes})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		return fmt.Errorf("write public key %q: %w", path, err)
	}
	return nil
}

// hmacSigner signs with HMAC-SHA256 using a shared secret.
type hmacSigner struct {
	key []byte
}

func loadHMACSigner(keyFile string) (*hmacSigner, error) {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read hmac key %q: %w", keyFile, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("hmac key %q is empty", keyFile)
	}
	return &hmacSigner{key: data}, nil
}

func (s *hmacSigner) Sign(data []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

func (s *hmacSigner) Algorithm() string { return "hmac-sha256" }
