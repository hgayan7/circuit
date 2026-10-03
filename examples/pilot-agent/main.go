// Minimal fixture agent. It receives only a Circuit credential and JSON input.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	url := flag.String("url", "", "Circuit URL")
	key := flag.String("key", "", "idempotency key")
	probe := flag.Bool("probe", false, "verify isolated runtime")
	caFile := flag.String("ca-cert", "", "Trusted staging CA PEM")
	tokenFile := flag.String("token-file", "", "Mounted Circuit agent credential")
	flag.Parse()
	if *probe {
		_, fileErr := os.ReadFile("/root/.config/gh/hosts.yml")
		for _, path := range []string{"/run/secrets/github-app.pem", "/run/secrets/admin-token", "/etc/circuit/gateway.yaml", "/var/lib/circuit/gateway.db"} {
			if _, err := os.ReadFile(path); err == nil {
				fmt.Fprintln(os.Stderr, "agent can read a gateway-only file")
				os.Exit(1)
			}
		}
		conn, networkErr := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
		if conn != nil {
			conn.Close()
		}
		ok := fileErr != nil && networkErr != nil && os.Getenv("GITHUB_TOKEN") == "" && os.Getenv("CIRCUIT_ADMIN_TOKEN") == "" && os.Getenv("CIRCUIT_PILOT_APP_KEY") == ""
		json.NewEncoder(os.Stdout).Encode(map[string]any{"isolated": ok, "upstream_egress_blocked": networkErr != nil, "provider_credentials_absent": os.Getenv("GITHUB_TOKEN") == "" && os.Getenv("CIRCUIT_PILOT_APP_KEY") == ""})
		if !ok {
			os.Exit(1)
		}
		return
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 512<<10))
	if err != nil {
		panic(err)
	}
	req, err := http.NewRequest("POST", *url+"/v1/actions", bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("CIRCUIT_AGENT_TOKEN"))
	if *tokenFile != "" {
		secret, err := os.ReadFile(*tokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read agent credential")
			os.Exit(1)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
	}
	req.Header.Set("Idempotency-Key", *key)
	req.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read staging CA")
			os.Exit(1)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			fmt.Fprintln(os.Stderr, "invalid staging CA")
			os.Exit(1)
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}
	}
	resp, err := (&http.Client{Timeout: 40 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
}
