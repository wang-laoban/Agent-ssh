package logger

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"log/slog"

	"test-agent/internal/config"
	"test-agent/internal/logentry"
)

func TestGenerateKeyPairAndVerifyEntry(t *testing.T) {
	privPath, pubPath, err := GenerateKeyPair("test-sign")
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	defer os.Remove(privPath)
	defer os.Remove(pubPath)

	pubKey, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}

	entry := logentry.Entry{
		Seq:      1,
		Level:    "INFO",
		Message:  "hello",
		PrevHash: "",
		Algo:     "ed25519",
	}

	// Use the logger's own signer to sign the entry.
	signer, err := LoadSigner("ed25519", privPath)
	if err != nil {
		t.Fatalf("LoadSigner failed: %v", err)
	}
	canonical, err := entry.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes failed: %v", err)
	}
	sig, err := signer.Sign(canonical)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	entry.Signature = hex.EncodeToString(sig)

	if err := VerifyEntry(pubKey, nil, entry); err != nil {
		t.Fatalf("VerifyEntry failed: %v", err)
	}

	// Tampered message should fail signature verification.
	entry.Message = "tampered"
	if err := VerifyEntry(pubKey, nil, entry); err == nil {
		t.Fatal("expected verification failure for tampered entry")
	}
}

func TestVerifyLogFile(t *testing.T) {
	privPath, pubPath, err := GenerateKeyPair("test-sign")
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	defer os.Remove(privPath)
	defer os.Remove(pubPath)

	pubKey, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}

	signer, err := LoadSigner("ed25519", privPath)
	if err != nil {
		t.Fatalf("LoadSigner failed: %v", err)
	}

	logFile := "test-verify.log"
	defer os.Remove(logFile)

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}

	var prevHash []byte
	for i := 1; i <= 3; i++ {
		entry := logentry.Entry{
			Seq:      uint64(i),
			Level:    "INFO",
			Message:  "entry",
			PrevHash: hex.EncodeToString(prevHash),
			Algo:     "ed25519",
		}
		canonical, _ := entry.CanonicalBytes()
		sig, _ := signer.Sign(canonical)
		entry.Signature = hex.EncodeToString(sig)
		prevHash = entry.Hash()
		if err := writeJSONLine(f, entry); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	_ = f.Close()

	if err := VerifyLogFile(logFile, pubKey); err != nil {
		t.Fatalf("VerifyLogFile failed: %v", err)
	}
}

func writeJSONLine(f *os.File, e logentry.Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

func TestConsoleOutput(t *testing.T) {
	privPath, pubPath, err := GenerateKeyPair("test-console-sign")
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	defer os.Remove(privPath)
	defer os.Remove(pubPath)

	logFile := "test-console.log"
	defer os.Remove(logFile)

	cfg := config.LogConfig{
		Enabled:       true,
		File:          logFile,
		SignKeyFile:   privPath,
		VerifyPubFile: pubPath,
		SignatureAlgo: "ed25519",
		MQ: config.MQConfig{
			Type: "noop",
		},
	}

	var console bytes.Buffer
	tpLogger, err := New(cfg, &console)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer tpLogger.Close()

	logger := slog.New(tpLogger)
	logger.Info("hello_console", slog.String("test_key", "test_value"))

	if console.Len() == 0 {
		t.Fatal("expected console output, got none")
	}

	lines := bytes.Split(bytes.TrimSpace(console.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected exactly one console line, got %d", len(lines))
	}

	var entry logentry.Entry
	if err := json.Unmarshal(lines[0], &entry); err != nil {
		t.Fatalf("console output is not valid JSON: %v", err)
	}
	if entry.Message != "hello_console" {
		t.Fatalf("expected message %q, got %q", "hello_console", entry.Message)
	}
	if entry.Level != "INFO" {
		t.Fatalf("expected level INFO, got %q", entry.Level)
	}
	if entry.Attrs["test_key"] != "test_value" {
		t.Fatalf("expected attr test_key=test_value, got %v", entry.Attrs["test_key"])
	}
	if entry.Signature == "" {
		t.Fatal("expected entry to be signed")
	}

	// Verify the on-disk log is still intact and verifiable.
	pubKey, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}
	if err := VerifyLogFile(logFile, pubKey); err != nil {
		t.Fatalf("VerifyLogFile failed: %v", err)
	}

	// Ensure file and console contain the same bytes (same JSON line).
	fileData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !bytes.Equal(fileData, console.Bytes()) {
		t.Fatalf("file and console output differ:\nfile=%q\nconsole=%q", fileData, console.Bytes())
	}
}

func TestConsoleOutputIgnoredOnError(t *testing.T) {
	privPath, pubPath, err := GenerateKeyPair("test-console-err-sign")
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	defer os.Remove(privPath)
	defer os.Remove(pubPath)

	logFile := "test-console-err.log"
	defer os.Remove(logFile)

	cfg := config.LogConfig{
		Enabled:       true,
		File:          logFile,
		SignKeyFile:   privPath,
		VerifyPubFile: pubPath,
		SignatureAlgo: "ed25519",
		MQ: config.MQConfig{
			Type: "noop",
		},
	}

	// A writer that always fails should not break the file log.
	tpLogger, err := New(cfg, &failWriter{})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer tpLogger.Close()

	logger := slog.New(tpLogger)
	logger.Info("should_still_persist_despite_console_error")

	pubKey, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}
	if err := VerifyLogFile(logFile, pubKey); err != nil {
		t.Fatalf("VerifyLogFile failed: %v", err)
	}
}

type failWriter struct{}

func (f *failWriter) Write(p []byte) (n int, err error) {
	return 0, os.ErrInvalid
}

func TestEnsureKeyPairGeneratesAndReuses(t *testing.T) {
	privPath := "test-ensure-sign.key"
	pubPath := "test-ensure-sign.pub"
	defer os.Remove(privPath)
	defer os.Remove(pubPath)

	// First call: keys are missing, so a new pair must be created.
	generated, err := EnsureKeyPair("ed25519", privPath, pubPath)
	if err != nil {
		t.Fatalf("EnsureKeyPair failed: %v", err)
	}
	if !generated {
		t.Fatal("expected generated=true on first call")
	}

	if _, err := os.Stat(privPath); err != nil {
		t.Fatalf("private key not written: %v", err)
	}
	if _, err := os.Stat(pubPath); err != nil {
		t.Fatalf("public key not written: %v", err)
	}

	// The generated key material must be usable by LoadSigner/LoadPublicKey.
	if _, err := LoadSigner("ed25519", privPath); err != nil {
		t.Fatalf("LoadSigner failed on generated key: %v", err)
	}
	if _, err := LoadPublicKey(pubPath); err != nil {
		t.Fatalf("LoadPublicKey failed on generated key: %v", err)
	}

	// Second call: existing keys must be left untouched.
	privBefore, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	generated, err = EnsureKeyPair("ed25519", privPath, pubPath)
	if err != nil {
		t.Fatalf("EnsureKeyPair second call failed: %v", err)
	}
	if generated {
		t.Fatal("expected generated=false when keys already exist")
	}
	privAfter, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatalf("read private key after: %v", err)
	}
	if !bytes.Equal(privBefore, privAfter) {
		t.Fatal("existing private key was overwritten")
	}
}

func TestEnsureKeyPairHMAC(t *testing.T) {
	keyPath := "test-ensure-hmac.key"
	defer os.Remove(keyPath)

	generated, err := EnsureKeyPair("hmac-sha256", keyPath, "")
	if err != nil {
		t.Fatalf("EnsureKeyPair failed: %v", err)
	}
	if !generated {
		t.Fatal("expected generated=true on first call")
	}

	signer, err := LoadSigner("hmac-sha256", keyPath)
	if err != nil {
		t.Fatalf("LoadSigner failed on generated hmac key: %v", err)
	}
	if signer.Algorithm() != "hmac-sha256" {
		t.Fatalf("unexpected algorithm %q", signer.Algorithm())
	}

	generated, err = EnsureKeyPair("hmac-sha256", keyPath, "")
	if err != nil {
		t.Fatalf("EnsureKeyPair second call failed: %v", err)
	}
	if generated {
		t.Fatal("expected generated=false when key already exists")
	}
}

func TestEnsureKeyPairUnsupportedAlgo(t *testing.T) {
	if _, err := EnsureKeyPair("rsa", "whatever.key", ""); err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
}
