package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hgayan7/circuit/pkg/telemetry"
	bolt "go.etcd.io/bbolt"
)

func operatorRouteAllowed(role, method, path string) bool {
	if role == "admin" {
		return true
	}
	if method == http.MethodGet && path != "/admin/backup" {
		return true
	}
	return role == "reviewer" && method == http.MethodPost && strings.HasSuffix(path, "/decision")
}

func (h *HTTPHandler) SetLogger(logger *slog.Logger) { h.logger = logger }
func (h *HTTPHandler) Drain()                        { h.draining.Store(true) }

// SetTLSExpiry records the certificate actually loaded by the TLS listener.
func (h *HTTPHandler) SetTLSExpiry(expiry time.Time) { h.tlsExpiry.Store(expiry.Unix()) }

type observedResponse struct {
	http.ResponseWriter
	status int
}

func (w *observedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *observedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (w *observedResponse) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}
func (h *HTTPHandler) observe(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	response := &observedResponse{ResponseWriter: w}
	admitted := r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
	if !admitted {
		select {
		case h.slots <- struct{}{}:
			admitted = true
			defer func() { <-h.slots }()
		default:
		}
	}
	if admitted {
		h.serveHTTP(response, r)
	} else {
		writeJSON(response, 503, map[string]string{"error": "Gateway request capacity reached"})
	}
	status := response.status
	if status == 0 {
		status = 200
	}
	h.requests.Add(1)
	if status >= 500 {
		h.errors.Add(1)
	}
	if h.logger != nil {
		// Never log raw paths, query strings, headers, bodies, or provider output.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		method := r.Method
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			method = "OTHER"
		}
		h.logger.Info("http_request", "method", method, "route", route, "status", status, "duration_ms", time.Since(start).Milliseconds())
	}
}
func (h *HTTPHandler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "alive"})
}
func (h *HTTPHandler) ready(w http.ResponseWriter, _ *http.Request) {
	if h.draining.Load() || h.service.store.Check() != nil {
		writeJSON(w, 503, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
func (h *HTTPHandler) me(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(identityKey{}).(principal)
	id := p.Operator
	if id == "" {
		id = "legacy-shared-operator"
	}
	writeJSON(w, 200, map[string]string{"id": id, "role": p.Role})
}
func (h *HTTPHandler) metrics(w http.ResponseWriter, _ *http.Request) {
	counts := map[string]int{"pending": 0, "approved": 0, "executing": 0, "uncertain": 0, "succeeded": 0, "failed": 0, "denied": 0, "rejected": 0, "expired": 0}
	err := h.service.store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(actionsBucket).ForEach(func(k, _ []byte) error {
			a, err := getAction(tx, string(k))
			if err != nil {
				return err
			}
			if _, ok := counts[a.State]; ok {
				counts[a.State]++
			}
			return nil
		})
	})
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "State metrics unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	telemetry.Filesystem(w, h.service.store.db.Path(), "state")
	telemetry.Filesystem(w, os.TempDir(), "snapshot_temp")
	if expiry := h.tlsExpiry.Load(); expiry != 0 {
		fmt.Fprintf(w, "circuit_tls_certificate_expiry_unix %d\n", expiry)
	}
	fmt.Fprintf(w, "# TYPE circuit_storage_unavailable gauge\ncircuit_storage_unavailable %d\n# TYPE circuit_restore_pending gauge\ncircuit_restore_pending %d\n", boolInt(h.service.store.unavailable.Load()), boolInt(h.service.store.restorePending.Load()))
	fmt.Fprintf(w, "# TYPE circuit_http_requests_total counter\ncircuit_http_requests_total %d\n# TYPE circuit_http_errors_total counter\ncircuit_http_errors_total %d\n# TYPE circuit_draining gauge\ncircuit_draining %d\n# TYPE circuit_actions gauge\n", h.requests.Load(), h.errors.Load(), boolInt(h.draining.Load()))
	for _, state := range []string{"pending", "approved", "executing", "uncertain", "succeeded", "failed", "denied", "rejected", "expired"} {
		fmt.Fprintf(w, "circuit_actions{state=%q} %d\n", state, counts[state])
	}
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func (h *HTTPHandler) backup(w http.ResponseWriter, r *http.Request) {
	// Snapshot to disk before streaming so slow clients do not pin a read transaction.
	f, err := h.service.store.backupTemp()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Backup failed"})
		return
	}
	defer removeTemp(f)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="circuit-backup.db"`)
	if _, err := f.Seek(0, 0); err != nil {
		writeJSON(w, 503, map[string]string{"error": "Backup failed"})
		return
	}
	// Stream only after a complete, verified snapshot exists.
	http.ServeContent(w, r, "circuit-backup.db", time.Time{}, f)
}
