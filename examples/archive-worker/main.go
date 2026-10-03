package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/hgayan7/circuit/pkg/archive"
)

type report struct {
	Updated     time.Time         `json:"updated_at"`
	LastSuccess int64             `json:"last_success_unix"`
	Failures    int               `json:"failures"`
	Receipts    []archive.Receipt `json:"receipts"`
}

func main() {
	config := flag.String("config", "/run/secrets/storage.conf", "Operator-owned rclone configuration")
	remote := flag.String("remote", "", "Fixed named storage remote and prefix")
	source := flag.String("source", "/backups", "Readonly encrypted archives")
	state := flag.String("state", "/var/lib/archive/report.json", "Private transfer evidence")
	interval := flag.Duration("interval", time.Hour, "Transfer and readback interval")
	once := flag.Bool("once", false, "One verified transfer pass")
	flag.Parse()
	backend, err := archive.NewRclone(*config, *remote)
	if err != nil || *interval < time.Second {
		fmt.Fprintln(os.Stderr, "invalid storage configuration")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	var mu sync.Mutex
	var status report
	initial, _ := json.Marshal(status)
	if err := save(*state, initial); err != nil {
		fmt.Fprintln(os.Stderr, "cannot initialize transfer evidence")
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "circuit_archive_last_success_unix %d\ncircuit_archive_failures_total %d\n", status.LastSuccess, status.Failures)
	})
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})
	server := &http.Server{Addr: ":9092", Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	if !*once {
		go func() {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				stop()
			}
		}()
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 10*time.Minute)
		receipts, err := archive.Sync(pass, *source, backend)
		cancel()
		mu.Lock()
		status.Updated = time.Now().UTC()
		if err != nil {
			status.Failures++
			fmt.Fprintln(os.Stderr, "encrypted archive transfer or readback failed")
		} else {
			status.LastSuccess = time.Now().Unix()
			status.Receipts = receipts
		}
		data, _ := json.MarshalIndent(status, "", "  ")
		mu.Unlock()
		if saveErr := save(*state, data); saveErr != nil {
			fmt.Fprintln(os.Stderr, "cannot persist transfer evidence")
			os.Exit(1)
		}
		if *once {
			if err != nil {
				os.Exit(1)
			}
			fmt.Println("Encrypted archive copies verified by full remote readback")
			return
		}
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			server.Shutdown(shutdown)
			cancel()
			return
		case <-ticker.C:
		}
	}
}
func save(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".archive-report-*")
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
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
