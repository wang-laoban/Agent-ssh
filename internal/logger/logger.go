package logger

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"test-agent/internal/config"
	"test-agent/internal/logentry"
	"test-agent/internal/mq"
)

// Entry is an alias for the shared structured log record.
type Entry = logentry.Entry

// TamperProofLogger implements slog.Handler and persists signed log entries.
// It combines file append-only storage with asynchronous MQ replication and
// optional console output.
type TamperProofLogger struct {
	cfg      config.LogConfig
	signer   Signer
	sender   mq.Sender
	minLevel slog.Level
	seq      atomic.Uint64
	lastHash []byte
	file     *os.File
	console  io.Writer
	queue    chan Entry
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
}

// New creates a tamper-proof logger from configuration.
// If console is non-nil, the same JSON log entries are also written to it.
func New(cfg config.LogConfig, console io.Writer) (*TamperProofLogger, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	signer, err := LoadSigner(cfg.SignatureAlgo, cfg.SignKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load signer: %w", err)
	}

	sender, err := mq.New(cfg.MQ)
	if err != nil {
		return nil, fmt.Errorf("create mq sender: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	file, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	tl := &TamperProofLogger{
		cfg:      cfg,
		signer:   signer,
		sender:   sender,
		minLevel: slog.LevelInfo,
		file:     file,
		console:  console,
		queue:    make(chan Entry, 4096),
		ctx:      ctx,
		cancel:   cancel,
	}

	// Recover the last hash from the existing log file to keep the chain intact.
	if err := tl.recoverLastHash(); err != nil {
		_ = file.Close()
		cancel()
		return nil, fmt.Errorf("recover last hash: %w", err)
	}

	tl.wg.Add(1)
	go tl.dispatchLoop()

	return tl, nil
}

// NewFallback returns a logger that writes to stderr when tamper-proof setup fails.
func NewFallback(err error) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("tamper_proof_init_error", err.Error())
}

// recoverLastHash reads the final line of the log file and extracts its hash.
func (t *TamperProofLogger) recoverLastHash() error {
	info, err := t.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		t.lastHash = nil
		return nil
	}

	data, err := os.ReadFile(t.cfg.File)
	if err != nil {
		return err
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) == 0 {
		t.lastHash = nil
		return nil
	}
	var last Entry
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		return fmt.Errorf("parse last log entry: %w", err)
	}
	t.seq.Store(last.Seq)
	t.lastHash = last.Hash()
	return nil
}

// Enabled reports whether the handler handles records at the given level.
func (t *TamperProofLogger) Enabled(_ context.Context, level slog.Level) bool {
	return level >= t.minLevel
}

// Handle implements slog.Handler.
func (t *TamperProofLogger) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]interface{}, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})

	entry := Entry{
		Timestamp: r.Time.UTC(),
		Level:     r.Level.String(),
		Message:   r.Message,
		Attrs:     attrs,
	}

	t.mu.Lock()
	entry.Seq = t.seq.Add(1)
	entry.PrevHash = hex.EncodeToString(t.lastHash)
	entry.Algo = t.signer.Algorithm()

	canonical, err := entry.CanonicalBytes()
	if err != nil {
		t.mu.Unlock()
		return fmt.Errorf("canonicalize entry: %w", err)
	}
	sig, err := t.signer.Sign(canonical)
	if err != nil {
		t.mu.Unlock()
		return fmt.Errorf("sign entry: %w", err)
	}
	entry.Signature = hex.EncodeToString(sig)

	t.lastHash = entry.Hash()
	t.mu.Unlock()

	// Write to local append-only log synchronously.
	if err := t.appendEntry(entry); err != nil {
		return err
	}

	// Enqueue for async MQ replication.
	select {
	case t.queue <- entry:
	default:
		// Queue full: drop MQ replica but keep local log intact.
	}

	return nil
}

// WithAttrs returns a new handler with the given attributes.
func (t *TamperProofLogger) WithAttrs(attrs []slog.Attr) slog.Handler {
	// For this implementation we intentionally keep the handler stateless
	// across attribute groups; all attributes are embedded in each record.
	return t
}

// WithGroup returns a new handler with the given group name.
func (t *TamperProofLogger) WithGroup(name string) slog.Handler {
	return t
}

// appendEntry writes one JSON line to the log file and, if configured, to the console.
func (t *TamperProofLogger) appendEntry(e Entry) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(e); err != nil {
		return fmt.Errorf("encode log entry: %w", err)
	}

	t.mu.Lock()
	if _, err := t.file.Write(buf.Bytes()); err != nil {
		t.mu.Unlock()
		return fmt.Errorf("write log entry: %w", err)
	}
	t.mu.Unlock()

	if t.console != nil {
		// Console output is best-effort; never let terminal errors break the file log.
		_, _ = t.console.Write(buf.Bytes())
	}

	return nil
}

// dispatchLoop forwards queued entries to the MQ sender.
func (t *TamperProofLogger) dispatchLoop() {
	defer t.wg.Done()
	for {
		select {
		case e := <-t.queue:
			ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
			_ = t.sender.Send(ctx, e)
			cancel()
		case <-t.ctx.Done():
			return
		}
	}
}

// Close flushes pending MQ messages and closes resources.
func (t *TamperProofLogger) Close() error {
	t.cancel()
	t.wg.Wait()

	// Drain remaining queue entries with a short grace period.
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		select {
		case e := <-t.queue:
			_ = t.sender.Send(drainCtx, e)
		case <-drainCtx.Done():
			goto done
		default:
			goto done
		}
	}
done:
	_ = t.sender.Close()
	return t.file.Close()
}

// GenerateKeyPair creates an Ed25519 signing key pair and writes PEM files.
func GenerateKeyPair(prefix string) (privPath, pubPath string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key: %w", err)
	}

	privPath = prefix + ".key"
	pubPath = prefix + ".pub"

	if err := writeEd25519PrivateKey(privPath, priv); err != nil {
		return "", "", err
	}
	if err := writeEd25519PublicKey(pubPath, priv.Public()); err != nil {
		return "", "", err
	}

	return privPath, pubPath, nil
}

// VerifyEntry checks a single entry's signature and its continuity with prevHash.
func VerifyEntry(pubKey ed25519.PublicKey, prevHash []byte, e Entry) error {
	canonical, err := e.CanonicalBytes()
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(e.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pubKey, canonical, sig) {
		return fmt.Errorf("signature verification failed for seq %d", e.Seq)
	}
	if e.Seq > 1 {
		expected := hex.EncodeToString(prevHash)
		if e.PrevHash != expected {
			return fmt.Errorf("hash chain broken at seq %d: expected prev_hash %s, got %s", e.Seq, expected, e.PrevHash)
		}
	}
	return nil
}

// VerifyLogFile validates every entry in a log file using the provided public key.
func VerifyLogFile(path string, pubKey ed25519.PublicKey) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var prevHash []byte
	var seq uint64
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decode entry: %w", err)
		}
		if e.Seq != seq+1 {
			return fmt.Errorf("sequence gap: expected %d, got %d", seq+1, e.Seq)
		}
		if err := VerifyEntry(pubKey, prevHash, e); err != nil {
			return err
		}
		seq = e.Seq
		prevHash = e.Hash()
	}
	return nil
}

// LoadPublicKey reads an Ed25519 public key from a PEM file.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an ed25519 public key")
	}
	return edPub, nil
}
