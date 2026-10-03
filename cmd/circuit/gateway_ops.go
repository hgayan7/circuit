package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/spf13/cobra"
)

func gatewayClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read CA certificate")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid CA certificate")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("gateway redirects are not allowed") }}, nil
}

func gatewayURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("gateway URL must be a root HTTP(S) origin without credentials")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return "", fmt.Errorf("non-loopback gateways require HTTPS")
	}
	return strings.TrimRight(base, "/") + path, nil
}

func addGatewayOperations(parent *cobra.Command) {
	var base, tokenFile, output, caFile string
	backup := &cobra.Command{Use: "backup", Short: "Download and verify a live private state snapshot (named admin required)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		endpoint, err := gatewayURL(base, "/admin/backup")
		if err != nil {
			return err
		}
		secret, err := os.ReadFile(tokenFile)
		if err != nil {
			return fmt.Errorf("cannot read operator token file")
		}
		client, err := gatewayClient(caFile)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(cmd.Context(), "GET", endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("backup request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("backup request returned HTTP %d", resp.StatusCode)
		}
		if err := gateway.SaveSnapshot(resp.Body, output, 1<<30); err != nil {
			return err
		}
		cmd.Printf("Verified private snapshot saved to %s\n", output)
		return nil
	}}
	backup.Flags().StringVar(&base, "url", "https://127.0.0.1:8443", "Gateway origin")
	backup.Flags().StringVar(&tokenFile, "token-file", "", "Operator-owned admin token file")
	backup.Flags().StringVar(&output, "out", "", "New snapshot path (never overwrite)")
	backup.Flags().StringVar(&caFile, "ca-cert", "", "Additional trusted CA certificate PEM")
	backup.MarkFlagRequired("token-file")
	backup.MarkFlagRequired("out")
	parent.AddCommand(backup)
	var source, destination string
	restore := &cobra.Command{Use: "restore", Short: "Verify and copy a backup to a NEW state path; stop gateway before switching", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := gateway.RestoreSnapshot(source, destination); err != nil {
			return err
		}
		cmd.Printf("Verified state restored to %s with dispatch paused. Reconcile provider activity since the snapshot, then acknowledge-restore offline; executing actions become uncertain.\n", destination)
		return nil
	}}
	restore.Flags().StringVar(&source, "backup", "", "Snapshot input")
	restore.Flags().StringVar(&destination, "out", "", "New private state file (never overwrite)")
	restore.MarkFlagRequired("backup")
	restore.MarkFlagRequired("out")
	parent.AddCommand(restore)
	var restoredData, recoveryNote string
	acknowledge := &cobra.Command{Use: "acknowledge-restore", Short: "Offline release of restored-state dispatch barrier after provider reconciliation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := gateway.AcknowledgeRestore(restoredData, recoveryNote); err != nil {
			return err
		}
		cmd.Println("Restore evidence recorded. Restart the single gateway instance to resume dispatch.")
		return nil
	}}
	acknowledge.Flags().StringVar(&restoredData, "data", "", "Restored state file; gateway must be stopped")
	acknowledge.Flags().StringVar(&recoveryNote, "note", "", "Provider reconciliation evidence (no secrets)")
	acknowledge.MarkFlagRequired("data")
	acknowledge.MarkFlagRequired("note")
	parent.AddCommand(acknowledge)
	var healthURL, healthCA string
	health := &cobra.Command{Use: "healthcheck", Short: "Probe readiness without credentials", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		endpoint, err := gatewayURL(healthURL, "/readyz")
		if err != nil {
			return err
		}
		client, err := gatewayClient(healthCA)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("gateway readiness unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("gateway is not ready (HTTP %d)", resp.StatusCode)
		}
		return nil
	}}
	health.Flags().StringVar(&healthURL, "url", "https://127.0.0.1:8443", "Gateway origin")
	health.Flags().StringVar(&healthCA, "ca-cert", "", "Additional trusted CA certificate")
	parent.AddCommand(health)
}
