// Separate operator and isolated-agent workers for the local deployment.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type report struct {
	Mode        string    `json:"mode"`
	Started     time.Time `json:"started_at"`
	Updated     time.Time `json:"updated_at"`
	Samples     int       `json:"samples"`
	Failures    int       `json:"failures"`
	LastSuccess int64     `json:"last_success_unix"`
	Elapsed     float64   `json:"elapsed_seconds"`
	Required    float64   `json:"required_seconds"`
	Complete    bool      `json:"complete"`
	MultiDay    bool      `json:"multi_day_soak_complete"`
	Scope       string    `json:"scope"`
}

var archiveName = regexp.MustCompile(`^circuit-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{16}\.db\.age$`)

func prune(dir string, retain int) error {
	if retain < 1 {
		return fmt.Errorf("retention must be positive")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !archiveName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > retain {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

func snapshot(ctx context.Context, base, ca, token, recipient, dir string, retain int) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	name := "circuit-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random) + ".db.age"
	cmd := exec.CommandContext(ctx, "circuit", "gateway", "backup", "--url", base, "--ca-cert", ca,
		"--token-file", token, "--recipient-file", recipient, "--out", filepath.Join(dir, name))
	// Child output may include paths; never include it in metrics or logs.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("encrypted backup failed")
	}
	return prune(dir, retain)
}

func sample(ctx context.Context, client *http.Client, base, token, repo string, pr, sequence int) error {
	probe, _ := http.NewRequestWithContext(ctx, "GET", base+"/readyz", nil)
	response, err := client.Do(probe)
	if err != nil {
		return fmt.Errorf("readiness unavailable")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("readiness failed")
	}
	credential, err := os.ReadFile(token)
	if err != nil {
		return fmt.Errorf("agent credential unavailable")
	}
	body, _ := json.Marshal(map[string]any{"operation": "get_pr", "repository": repo, "args": map[string]int{"number": pr}})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/v1/actions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("soak-%d-%d", time.Now().UnixNano(), sequence))
	response, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub read unavailable")
	}
	defer response.Body.Close()
	var action struct {
		State string `json:"state"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&action) != nil || action.State != "succeeded" {
		return fmt.Errorf("GitHub read failed")
	}
	return nil
}

func main() {
	mode := flag.String("mode", "", "backup or soak")
	base := flag.String("url", "https://gateway:8443", "Gateway origin")
	ca := flag.String("ca-cert", "/run/secrets/tls.crt", "Trusted CA")
	token := flag.String("token-file", "", "Role-specific token file")
	recipient := flag.String("recipient-file", "/run/secrets/backup-recipient", "Public age recipient")
	directory := flag.String("backup-dir", "/backups", "Encrypted archive directory")
	retain := flag.Int("retain", 168, "Maximum successful hourly archives")
	interval := flag.Duration("interval", time.Hour, "Sample or backup interval")
	duration := flag.Duration("duration", 72*time.Hour, "Required uninterrupted soak")
	repo := flag.String("repo", "", "Authorized fixture repository")
	pr := flag.Int("pr", 3, "Harmless fixture PR read")
	state := flag.String("state", "/var/lib/circuit-ops/report.json", "Private durable report path")
	listen := flag.String("listen", ":9091", "Internal monitoring listener")
	flag.Parse()
	if (*mode != "backup" && *mode != "soak") || *interval < time.Second || *duration < time.Second || *retain < 1 || !strings.HasPrefix(*base, "https://") || *token == "" {
		fmt.Fprintln(os.Stderr, "invalid worker configuration")
		os.Exit(1)
	}
	if *mode == "soak" && *repo != "hgayan7/circuit-gateway-pilot-20261003" && *repo != "hgayan7/circuit-gateway-protection-pilot-20261003" {
		fmt.Fprintln(os.Stderr, "soak permits only authorized fixture repositories")
		os.Exit(1)
	}
	pem, err := os.ReadFile(*ca)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CA unavailable")
		os.Exit(1)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		fmt.Fprintln(os.Stderr, "invalid CA")
		os.Exit(1)
	}
	client := &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	started := time.Now()
	r := report{Mode: *mode, Started: started.UTC(), Required: duration.Seconds(), Scope: "local Docker; fixture GitHub reads only; no write approvals"}
	initial, _ := json.MarshalIndent(r, "", "  ")
	if err := saveReport(*state, initial); err != nil {
		fmt.Fprintln(os.Stderr, "cannot initialize fresh worker report")
		os.Exit(1)
	}
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "circuit_worker_last_success_unix %d\ncircuit_worker_failures_total %d\ncircuit_worker_samples_total %d\ncircuit_worker_complete %d\n", r.LastSuccess, r.Failures, r.Samples, boolInt(r.Complete))
	})
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r)
	})
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "worker monitoring listener failed")
			stop()
		}
	}()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	previous := time.Now()
	for {
		workCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		var err error
		if *mode == "backup" {
			err = snapshot(workCtx, *base, *ca, *token, *recipient, *directory, *retain)
		} else {
			err = sample(workCtx, client, *base, *token, *repo, *pr, r.Samples)
		}
		cancel()
		now := time.Now()
		mu.Lock()
		if now.Sub(previous) > 2*(*interval)+45*time.Second {
			r.Failures++
		}
		previous = now
		r.Samples++
		if err != nil {
			r.Failures++
			fmt.Fprintln(os.Stderr, "worker sample failed")
		} else {
			r.LastSuccess = now.Unix()
		}
		r.Updated = now.UTC()
		r.Elapsed = time.Since(started).Seconds()
		if *mode == "soak" && time.Since(started) >= *duration {
			r.Complete = r.Failures == 0
			r.MultiDay = r.Complete && *duration >= 72*time.Hour
		}
		data, _ := json.MarshalIndent(r, "", "  ")
		mu.Unlock()
		if err := saveReport(*state, data); err != nil {
			fmt.Fprintln(os.Stderr, "cannot persist worker report")
			stop()
		}
		if *mode == "soak" && time.Since(started) >= *duration {
			<-ctx.Done()
			break
		}
		select {
		case <-ctx.Done():
			goto done
		case <-ticker.C:
		}
	}
done:
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func saveReport(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
