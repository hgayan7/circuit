package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/himshikhargayan/circuit/pkg/config"
)

// Entry represents a single audited transaction event.
type Entry struct {
	ID         string            `json:"id"`
	Timestamp  time.Time         `json:"timestamp"`
	Tool       string            `json:"tool,omitempty"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Method     string            `json:"method,omitempty"`
	Path       string            `json:"path,omitempty"`
	Args       map[string]any    `json:"args,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	AgentID    string            `json:"agent_id,omitempty"`
	Decision   config.ActionType `json:"decision"`
	RuleID     string            `json:"rule_id,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Duration   time.Duration     `json:"duration_ns"`
	ApprovedBy string            `json:"approved_by,omitempty"`
}

// Recorder defines the contract for recording audit entries.
type Recorder interface {
	Record(entry *Entry) error
	Close() error
}

// JSONRecorder streams structured JSON-lines to an io.Writer.
type JSONRecorder struct {
	mu     sync.Mutex
	writer io.Writer
	closer io.Closer
}

// NewJSONRecorder creates an audit recorder streaming to an io.Writer.
func NewJSONRecorder(w io.Writer) *JSONRecorder {
	return &JSONRecorder{
		writer: w,
	}
}

// NewFileRecorder creates an audit recorder appending to a file.
func NewFileRecorder(filePath string) (*JSONRecorder, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}
	return &JSONRecorder{
		writer: file,
		closer: file,
	}, nil
}

// Record marshals and writes the entry as a single JSON line.
func (r *JSONRecorder) Record(entry *Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal audit entry: %w", err)
	}

	data = append(data, '\n')
	_, err = r.writer.Write(data)
	return err
}

// Close closes the underlying file if applicable.
func (r *JSONRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}
