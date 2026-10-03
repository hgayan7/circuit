package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/spf13/cobra"
)

func init() {
	gatewayCmd := &cobra.Command{Use: "gateway", Short: "Scoped GitHub actions with durable limits and operator approvals"}
	rootCmd.AddCommand(gatewayCmd)
	gatewayCmd.AddCommand(&cobra.Command{Use: "token", Short: "Generate a Circuit bearer credential", RunE: func(cmd *cobra.Command, args []string) error {
		data := make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			return err
		}
		cmd.Println(base64.RawURLEncoding.EncodeToString(data))
		return nil
	}})
	var initPath, repository string
	initCmd := &cobra.Command{Use: "init", Short: "Write a GitHub gateway configuration", RunE: func(cmd *cobra.Command, args []string) error {
		config := gateway.ExampleConfig(repository)
		if _, err := gateway.ParseConfig(strings.NewReader(config)); err != nil {
			return err
		}
		f, err := os.OpenFile(initPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err = f.WriteString(config); err != nil {
			return err
		}
		cmd.Printf("Created %s. Set the three credential environment variables, then run circuit gateway serve.\n", initPath)
		return nil
	}}
	initCmd.Flags().StringVar(&initPath, "out", "gateway.yaml", "New configuration path (will not overwrite)")
	initCmd.Flags().StringVar(&repository, "repo", "owner/repository", "Allowed owner/repository")
	gatewayCmd.AddCommand(initCmd)
	var configPath, dataPath, listen string
	serve := &cobra.Command{Use: "serve", Short: "Serve the GitHub action API, MCP endpoint, and approval interface", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := gateway.LoadConfig(configPath)
		if err != nil {
			return err
		}
		var provider gateway.TokenProvider
		if cfg.GitHubApp != nil {
			var keyData []byte
			if cfg.GitHubApp.PrivateKeyEnv != "" {
				keyData = []byte(os.Getenv(cfg.GitHubApp.PrivateKeyEnv))
				if len(keyData) == 0 {
					return fmt.Errorf("set %s to the GitHub App private key PEM", cfg.GitHubApp.PrivateKeyEnv)
				}
			} else if cfg.GitHubApp.PrivateKeyFile != "" {
				var err error
				keyData, err = os.ReadFile(cfg.GitHubApp.PrivateKeyFile)
				if err != nil {
					return fmt.Errorf("reading GitHub App private key: %w", err)
				}
			}
			appProvider, err := gateway.NewGitHubAppTokenProvider(cfg.GitHubApp.AppID, keyData, cfg.GitHubApp.InstallationID)
			if err != nil {
				return fmt.Errorf("initializing GitHub App authentication: %w", err)
			}
			provider = appProvider
		} else {
			githubToken := os.Getenv(cfg.GitHubTokenEnv)
			if githubToken == "" {
				return fmt.Errorf("set %s to a scoped GitHub credential on the gateway only", cfg.GitHubTokenEnv)
			}
			provider = gateway.NewStaticTokenProvider(githubToken)
		}
		if err := os.MkdirAll(filepath.Dir(dataPath), 0700); err != nil {
			return err
		}
		store, err := gateway.OpenStore(dataPath)
		if err != nil {
			return err
		}
		defer store.Close()
		service, err := gateway.NewService(cfg, store, gateway.NewGitHubWithProvider(provider))
		if err != nil {
			return err
		}
		var webhookSecret string
		if cfg.Webhook != nil && cfg.Webhook.SecretEnv != "" {
			webhookSecret = os.Getenv(cfg.Webhook.SecretEnv)
		}
		tokens := gateway.Tokens{
			Admin:         os.Getenv(cfg.AdminTokenEnv),
			Agents:        map[string]string{},
			WebhookSecret: webhookSecret,
		}
		for _, a := range cfg.Agents {
			tokens.Agents[a.ID] = os.Getenv(a.TokenEnv)
		}
		handler, err := gateway.NewHTTPHandler(service, tokens)
		if err != nil {
			return err
		}
		return serveGateway(cmd, listen, handler)
	}}
	serve.Flags().StringVar(&configPath, "config", "gateway.yaml", "Gateway configuration")
	serve.Flags().StringVar(&dataPath, "data", ".circuit/gateway.db", "Durable state file")
	serve.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "Listen address; keep on loopback or use an authenticated TLS deployment")
	gatewayCmd.AddCommand(serve)
	check := &cobra.Command{Use: "check [gateway.yaml]", Args: cobra.MaximumNArgs(1), Short: "Validate GitHub scope, limits, and combined policy rules", RunE: func(cmd *cobra.Command, args []string) error {
		path := "gateway.yaml"
		if len(args) > 0 {
			path = args[0]
		}
		cfg, err := gateway.LoadConfig(path)
		if err != nil {
			return err
		}
		authMode := "token"
		if cfg.GitHubApp != nil {
			authMode = "github-app"
		}
		webhookMsg := "disabled"
		if cfg.Webhook != nil {
			webhookMsg = cfg.Webhook.Path
		}
		cmd.Printf("Gateway %q validated: auth=%s, webhook=%s, %d agents, %d rules, %d limits.\n", cfg.Name, authMode, webhookMsg, len(cfg.Agents), len(cfg.Rules), len(cfg.Limits))
		return nil
	}}
	gatewayCmd.AddCommand(check)
	var demoListen string
	demo := &cobra.Command{Use: "demo", Short: "Run a local simulated GitHub workflow without real credentials", RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := os.MkdirTemp("", "circuit-gateway-demo-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		cfg, err := gateway.ParseConfig(strings.NewReader(gateway.ExampleConfig("demo/project")))
		if err != nil {
			return err
		}
		store, err := gateway.OpenStore(filepath.Join(dir, "state.db"))
		if err != nil {
			return err
		}
		defer store.Close()
		service, err := gateway.NewService(cfg, store, &gateway.DemoExecutor{})
		if err != nil {
			return err
		}
		handler, err := gateway.NewHTTPHandler(service, gateway.Tokens{Admin: gateway.DemoAdminToken, Agents: map[string]string{"engineering-agent": gateway.DemoAgentToken}})
		if err != nil {
			return err
		}
		cmd.PrintErrln("SIMULATION: no requests are sent to GitHub. State is temporary.")
		cmd.PrintErrf("Operator token: %s\nAgent token: %s\n", gateway.DemoAdminToken, gateway.DemoAgentToken)
		return serveGateway(cmd, demoListen, handler)
	}}
	demo.Flags().StringVar(&demoListen, "listen", "127.0.0.1:8080", "Local demo listen address")
	gatewayCmd.AddCommand(demo)
}
func serveGateway(cmd *cobra.Command, address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	cmd.PrintErrf("Circuit GitHub gateway: http://%s\nApproval interface: / · MCP: /mcp\n", listener.Addr())
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 35*time.Second)
		defer c()
		server.Shutdown(shutdown)
	}()

	err = server.Serve(listener)
	cancel()
	<-shutdownDone
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
