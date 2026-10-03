// Pilot support only: opt-in GitHub App authentication and fault injection.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hgayan7/circuit/pkg/gateway"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

type lostResponse struct{ upstream gateway.Executor }

func (e lostResponse) Execute(ctx context.Context, r gateway.Request) gateway.Outcome {
	out := e.upstream.Execute(ctx, r)
	if r.Operation == "merge_pr" && out.Status == 200 && out.Error == "" {
		return gateway.Outcome{Uncertain: true, Error: "Pilot deliberately discarded successful merge response"}
	}
	return out
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "info", "info, token, hook, deliveries, or serve")
	repo := flag.String("repo", "", "repository for token")
	hookURL := flag.String("url", "", "webhook URL")
	cfgPath := flag.String("config", "", "gateway config")
	statePath := flag.String("data", "", "gateway state")
	flag.Parse()
	id, err := strconv.ParseInt(os.Getenv("CIRCUIT_PILOT_APP_ID"), 10, 64)
	if err != nil {
		return err
	}
	keyData, err := os.ReadFile(os.Getenv("CIRCUIT_PILOT_APP_KEY"))
	if err != nil {
		return err
	}
	provider, err := gateway.NewGitHubAppTokenProvider(id, keyData, 0)
	if err != nil {
		return err
	}
	if *mode == "token" {
		token, err := provider.Token(context.Background(), *repo)
		if err != nil {
			return err
		}
		fmt.Print(token)
		return nil
	}
	if *mode == "serve" {
		cfg, err := gateway.LoadConfig(*cfgPath)
		if err != nil {
			return err
		}
		store, err := gateway.OpenStore(*statePath)
		if err != nil {
			return err
		}
		defer store.Close()
		svc, err := gateway.NewService(cfg, store, lostResponse{gateway.NewGitHubWithProvider(provider)})
		if err != nil {
			return err
		}
		tokens := gateway.Tokens{Admin: os.Getenv("CIRCUIT_ADMIN_TOKEN"), Agents: map[string]string{"pilot": os.Getenv("CIRCUIT_AGENT_TOKEN")}, WebhookSecret: os.Getenv("CIRCUIT_PILOT_WEBHOOK_SECRET")}
		handler, err := gateway.NewHTTPHandler(svc, tokens)
		if err != nil {
			return err
		}
		mainServer := &http.Server{Addr: "127.0.0.1:55441", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		// The public tunnel reaches only this exact signed webhook route.
		public := &http.Server{Addr: "127.0.0.1:55442", ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/webhooks/github" {
				http.NotFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		})}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		failures := make(chan error, 2)
		go func() { failures <- mainServer.ListenAndServe() }()
		go func() { failures <- public.ListenAndServe() }()
		select {
		case err = <-failures:
		case <-ctx.Done():
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mainServer.Shutdown(shutdown)
		public.Shutdown(shutdown)
		return err
	}
	key, err := gateway.ParseRSAPrivateKey(keyData)
	if err != nil {
		return err
	}
	jwt, err := gateway.MintAppJWT(id, key, time.Now())
	if err != nil {
		return err
	}
	call := func(method, path string, payload any) ([]byte, error) {
		var body io.Reader
		if payload != nil {
			data, _ := json.Marshal(payload)
			body = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, "https://api.github.com"+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("GitHub %s returned %d", path, resp.StatusCode)
		}
		return data, nil
	}
	switch *mode {
	case "info":
		data, err := call("GET", "/app/installations", nil)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case "hook":
		secret := os.Getenv("CIRCUIT_PILOT_WEBHOOK_SECRET")
		if secret == "" || !strings.HasPrefix(*hookURL, "https://") {
			return fmt.Errorf("HTTPS URL and secret are required")
		}
		_, err = call("PATCH", "/app/hook/config", map[string]any{"url": *hookURL, "secret": secret, "content_type": "json", "insecure_ssl": "0"})
		if err != nil {
			return err
		}
		fmt.Println("Webhook configured; secret withheld")
	case "deliveries":
		data, err := call("GET", "/app/hook/deliveries", nil)
		if err != nil {
			return err
		}
		var deliveries []map[string]any
		if err = json.Unmarshal(data, &deliveries); err != nil {
			return err
		}
		for _, d := range deliveries {
			fmt.Printf("event=%v status=%v delivered_at=%v id=%v\n", d["event"], d["status_code"], d["delivered_at"], d["id"])
		}
	default:
		return fmt.Errorf("unknown mode")
	}
	return nil
}
