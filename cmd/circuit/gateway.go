package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
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
	addGatewayOperations(gatewayCmd)
	addGatewayForwarding(gatewayCmd)
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
	var configPath, dataPath, listen, tlsCert, tlsKey string
	var production bool
	serve := &cobra.Command{Use: "serve", Short: "Serve the action API, MCP endpoint, and approval interface", RunE: func(cmd *cobra.Command, args []string) error {
		return runGateway(cmd, configPath, dataPath, listen, tlsCert, tlsKey, production)
	}}
	serve.Flags().StringVar(&configPath, "config", "gateway.yaml", "Gateway configuration")
	serve.Flags().StringVar(&dataPath, "data", ".circuit/gateway.db", "Durable state file")
	serve.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "Listen address; keep on loopback or use an authenticated TLS deployment")
	serve.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate PEM")
	serve.Flags().StringVar(&tlsKey, "tls-key", "", "TLS private key PEM")
	serve.Flags().BoolVar(&production, "production", false, "Enforce the GitHub-only production deployment profile (TLS, named operators, GitHub App)")
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
		} else if cfg.GitHubTokenEnv == "" {
			authMode = "none"
		}
		webhookMsg := "disabled"
		if cfg.Webhook != nil {
			webhookMsg = cfg.Webhook.Path
		}
		cmd.Printf("Gateway %q validated: auth=%s, webhook=%s, %d workspaces, %d databases, %d environments, %d communications, %d payment accounts, %d custom tools, %d agents, %d rules, %d limits.\n", cfg.Name, authMode, webhookMsg, len(cfg.Workspaces), len(cfg.Databases), len(cfg.Environments), len(cfg.Communications), len(cfg.PaymentAccounts), len(cfg.CustomTools), len(cfg.Agents), len(cfg.Rules), len(cfg.Limits))
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
func runGateway(cmd *cobra.Command, configPath, dataPath, listen, tlsCert, tlsKey string, production bool) error {
	cfg, err := gateway.LoadConfig(configPath)
	if err != nil {
		return err
	}
	// Setup workspace executors
	if production {
		if err := cfg.ValidateProduction(); err != nil {
			return err
		}
		if tlsCert == "" || tlsKey == "" {
			return fmt.Errorf("production profile requires --tls-cert and --tls-key")
		}
	}
	wsExecutors := map[string]*gateway.ShellExecutor{}
	for _, wsCfg := range cfg.Workspaces {
		ws, err := gateway.NewWorkspace(wsCfg.ID, wsCfg.Path, wsCfg.ReadOnly, wsCfg.Timeout())
		if err != nil {
			return fmt.Errorf("initializing workspace %s: %w", wsCfg.ID, err)
		}
		wsExecutors[wsCfg.ID] = gateway.NewShellExecutor(ws)
		defer ws.Close()
	}

	// Setup database executors
	dbExecutors := map[string]*gateway.DatabaseExecutor{}
	for _, dbCfg := range cfg.Databases {
		dsn := dbCfg.DSN
		if dsn == "" && dbCfg.DSNEnv != "" {
			dsn = os.Getenv(dbCfg.DSNEnv)
		}
		target, err := gateway.NewDatabaseTarget(
			dbCfg.ID,
			dbCfg.Driver,
			dsn,
			dbCfg.ReadOnly,
			dbCfg.MaxRows,
			dbCfg.Timeout(),
			dbCfg.AllowTables,
			dbCfg.DenyTables,
		)
		if err != nil {
			return fmt.Errorf("initializing database %s: %w", dbCfg.ID, err)
		}
		dbExecutors[dbCfg.ID] = gateway.NewDatabaseExecutor(target)
		if target.DB != nil {
			defer target.DB.Close()
		}
	}

	// Setup cloud environment executors
	cloudExecutors := map[string]*gateway.CloudExecutor{}
	for _, envCfg := range cfg.Environments {
		env, err := gateway.NewCloudEnvironment(
			envCfg.ID,
			envCfg.Name,
			envCfg.Production,
			envCfg.AllowedServices,
			envCfg.MinReplicas,
			envCfg.MaxReplicas,
			envCfg.Timeout(),
		)
		if err != nil {
			return fmt.Errorf("initializing cloud environment %s: %w", envCfg.ID, err)
		}
		cloudExecutors[envCfg.ID] = gateway.NewCloudExecutor(env)
	}

	// Setup communication executors
	commExecutors := map[string]*gateway.CommExecutor{}
	for _, commCfg := range cfg.Communications {
		target, err := gateway.NewCommTarget(
			commCfg.ID,
			commCfg.Kind,
			commCfg.AllowedChannels,
			commCfg.InternalDomains,
			commCfg.MaxRecipients,
			commCfg.RequireApprovalForExternal,
		)
		if err != nil {
			return fmt.Errorf("initializing communication target %s: %w", commCfg.ID, err)
		}
		commExecutors[commCfg.ID] = gateway.NewCommExecutor(target)
	}

	// Setup payment executors
	paymentExecutors := map[string]*gateway.PaymentExecutor{}
	for _, payCfg := range cfg.PaymentAccounts {
		acc, err := gateway.NewPaymentAccount(
			payCfg.ID,
			payCfg.Name,
			payCfg.Currency,
			payCfg.AllowedDestinations,
			payCfg.MaxTransactionAmount,
			payCfg.AutoApprovalThreshold,
			payCfg.RequireApprovalForRefunds,
			payCfg.InitialBalance,
			payCfg.Timeout(),
		)
		if err != nil {
			return fmt.Errorf("initializing payment account %s: %w", payCfg.ID, err)
		}
		paymentExecutors[payCfg.ID] = gateway.NewPaymentExecutor(acc)
	}

	// Setup custom tool executors
	customExecutors := map[string]*gateway.CustomToolExecutor{}
	for _, ctCfg := range cfg.CustomTools {
		target, err := gateway.NewCustomToolTarget(
			ctCfg.ID,
			ctCfg.Name,
			ctCfg.Endpoint,
			ctCfg.Method,
			ctCfg.Headers,
			ctCfg.Operations,
			ctCfg.RequireApproval,
			ctCfg.Timeout(),
		)
		if err != nil {
			return fmt.Errorf("initializing custom tool %s: %w", ctCfg.ID, err)
		}
		if err := target.ConfigurePlugin(ctCfg); err != nil {
			return fmt.Errorf("initializing plugin %s: %w", ctCfg.ID, err)
		}
		customExecutors[ctCfg.ID] = gateway.NewCustomToolExecutor(target)
	}

	var githubExecutor gateway.Executor
	githubToken := os.Getenv(cfg.GitHubTokenEnv)
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
		githubExecutor = gateway.NewGitHubWithProvider(appProvider)
	} else if githubToken != "" {
		githubExecutor = gateway.NewGitHub(githubToken)
	}

	if githubExecutor == nil && len(wsExecutors) == 0 && len(dbExecutors) == 0 && len(cloudExecutors) == 0 && len(commExecutors) == 0 && len(paymentExecutors) == 0 && len(customExecutors) == 0 {
		return fmt.Errorf("gateway requires either GitHub credentials (%s / github_app), at least one workspace, at least one database, at least one cloud environment, at least one communication target, at least one payment account, or at least one custom tool configured", cfg.GitHubTokenEnv)
	}

	if err := os.MkdirAll(filepath.Dir(dataPath), 0700); err != nil {
		return err
	}
	store, err := gateway.OpenStore(dataPath)
	if err != nil {
		return err
	}
	defer store.Close()
	service, err := gateway.NewService(cfg, store, gateway.NewRouterExecutor(githubExecutor, wsExecutors, dbExecutors, cloudExecutors, commExecutors, paymentExecutors, customExecutors))
	if err != nil {
		return err
	}
	tokens, err := gateway.LoadTokens(cfg)
	if err != nil {
		return err
	}
	handler, err := gateway.NewHTTPHandler(service, tokens)
	if err != nil {
		return err
	}
	handler.SetLogger(slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil)))
	return serveGatewayTLS(cmd, listen, handler, tlsCert, tlsKey)
}

func serveGateway(cmd *cobra.Command, address string, handler http.Handler) error {
	return serveGatewayTLS(cmd, address, handler, "", "")
}
func serveGatewayTLS(cmd *cobra.Command, address string, handler http.Handler, cert, key string) error {
	if (cert == "") != (key == "") {
		return fmt.Errorf("both TLS certificate and key are required")
	}
	var tlsConfig *tls.Config
	if cert != "" {
		certificate, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return fmt.Errorf("cannot load TLS certificate and key")
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
		if observable, ok := handler.(interface{ SetTLSExpiry(time.Time) }); ok {
			leaf, err := x509.ParseCertificate(certificate.Certificate[0])
			if err != nil {
				return fmt.Errorf("cannot parse active TLS certificate")
			}
			observable.SetTLSExpiry(leaf.NotAfter)
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
		listener = tls.NewListener(listener, tlsConfig)
	}
	cmd.PrintErrf("Circuit gateway: %s://%s\nApproval interface: / · MCP: /mcp\n", scheme, listener.Addr())
	server := &http.Server{Handler: handler, MaxHeaderBytes: 16 << 10, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		if drainable, ok := handler.(interface{ Drain() }); ok {
			drainable.Drain()
		}
		shutdown, c := context.WithTimeout(context.Background(), 35*time.Second)
		defer c()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
		}
	}()

	err = server.Serve(listener)
	cancel()
	<-shutdownDone
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
