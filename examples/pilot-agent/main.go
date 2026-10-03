// Minimal fixture agent. It receives only a Circuit credential and JSON input.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	url := flag.String("url", "", "Circuit URL")
	key := flag.String("key", "", "idempotency key")
	probe := flag.Bool("probe", false, "verify isolated runtime")
	flag.Parse()
	if *probe {
		_, fileErr := os.ReadFile("/root/.config/gh/hosts.yml")
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
	req.Header.Set("Idempotency-Key", *key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
}
