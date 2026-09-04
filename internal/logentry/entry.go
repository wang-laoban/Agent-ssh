// Package logentry contains the shared structured log record used by the
// tamper-proof logger and the MQ replication layer.
package logentry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Entry is a single tamper-proof structured log record.
type Entry struct {
	Seq       uint64                 `json:"seq"`
	Timestamp time.Time              `json:"ts"`
	Level     string                 `json:"level"`
	Message   string                 `json:"msg"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"`
	PrevHash  string                 `json:"prev_hash"`
	Algo      string                 `json:"algo"`
	Signature string                 `json:"signature"`
}

// CanonicalBytes returns a deterministic serialization of the entry excluding
// the signature itself.
func (e Entry) CanonicalBytes() ([]byte, error) {
	canonical := struct {
		Seq       uint64                 `json:"seq"`
		Timestamp time.Time              `json:"ts"`
		Level     string                 `json:"level"`
		Message   string                 `json:"msg"`
		Attrs     map[string]interface{} `json:"attrs,omitempty"`
		PrevHash  string                 `json:"prev_hash"`
		Algo      string                 `json:"algo"`
	}{
		Seq:       e.Seq,
		Timestamp: e.Timestamp,
		Level:     e.Level,
		Message:   e.Message,
		Attrs:     e.Attrs,
		PrevHash:  e.PrevHash,
		Algo:      e.Algo,
	}
	return json.Marshal(canonical)
}

// Hash computes the chained SHA256 digest of the entry.
func (e Entry) Hash() []byte {
	data, _ := e.CanonicalBytes()
	h := sha256.New()
	h.Write(data)
	h.Write([]byte(e.Signature))
	return h.Sum(nil)
}

// HashString returns the hex-encoded chained hash.
func (e Entry) HashString() string {
	return hex.EncodeToString(e.Hash())
}
