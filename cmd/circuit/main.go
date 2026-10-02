package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/hgayan7/circuit/pkg/approval"
	"github.com/hgayan7/circuit/pkg/audit"
	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/interceptor/mcp"
	"github.com/hgayan7/circuit/pkg/interceptor/proxy"
	"github.com/hgayan7/circuit/pkg/policy"
	"github.com/hgayan7/circuit/pkg/runner"
	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"

	policyPath  string
	auditPath   string
	port        int
	targetURL   string
	injectToken string
	toolPrefix  string
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "circuit",
	Short: "Circuit: The Circuit Breaker & Safety Proxy for AI Agents",
	Long: `Circuit is an ultra-fast, zero-trust security sidecar and gateway 
designed for AI agents and Model Context Protocol (MCP) tooling. 
It intercepts outbound tool calls and APIs, enforcing argument-level policies, 
cumulative blast-radius budgets, and human-in-the-loop approvals.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the Circuit version",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Printf("circuit v%s\n", version)
	},
}

var checkCmd = &cobra.Command{
	Use:     "check [policy.yaml]",
	Aliases: []string{"validate"},
	Short:   "Lint and validate policy syntax and compile CEL rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		targetFile := policyPath
		if targetFile == "" && len(args) > 0 {
			targetFile = args[0]
		}
		if targetFile == "" {
			targetFile = "circuit.yaml"
		}

		pol, err := config.LoadPolicyFile(targetFile)
		if err != nil {
			return fmt.Errorf("failed to load policy: %w", err)
		}

		_, err = policy.NewEngine(pol)
		if err != nil {
			return fmt.Errorf("policy compilation failed: %w", err)
		}

		cmd.Printf("✅ Policy '%s' (version %s) compiled successfully with %d rule(s).\n",
			pol.Name, pol.Version, len(pol.Rules))
		return nil
	},
}

var runCmd = &cobra.Command{
	Use:   "run --policy <file> -- <command> [args...]",
	Short: "Run an agent command with automatic proxy injection",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetFile := policyPath
		if targetFile == "" {
			targetFile = "circuit.yaml"
		}

		pol, err := config.LoadPolicyFile(targetFile)
		if err != nil {
			return fmt.Errorf("failed to load policy: %w", err)
		}

		engine, err := policy.NewEngine(pol)
		if err != nil {
			return fmt.Errorf("policy engine initialization failed: %w", err)
		}

		var opts []runner.Option
		if targetURL != "" {
			u, err := url.Parse(targetURL)
			if err != nil {
				return fmt.Errorf("invalid target URL: %w", err)
			}
			opts = append(opts, runner.WithTargetURL(u))
		}
		if injectToken != "" {
			opts = append(opts, runner.WithInjectedToken(injectToken))
		}

		cliApprover := approval.NewCLIProvider(os.Stdin, os.Stderr)
		opts = append(opts, runner.WithApprovalProvider(cliApprover))

		if auditPath != "" {
			rec, err := audit.NewFileRecorder(auditPath)
			if err != nil {
				return fmt.Errorf("failed to initialize audit log: %w", err)
			}
			defer rec.Close()
			opts = append(opts, runner.WithAuditRecorder(rec))
		}

		r := runner.New(engine, opts...)
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return r.RunCommand(ctx, os.Stdin, os.Stdout, os.Stderr, args[0], args[1:]...)
	},
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage and wrap Model Context Protocol (MCP) servers",
}

var mcpWrapCmd = &cobra.Command{
	Use:   "wrap --policy <file> -- <command> [args...]",
	Short: "Wrap an MCP server stdio process with wire-level policy enforcement",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetFile := policyPath
		if targetFile == "" {
			targetFile = "circuit.yaml"
		}

		pol, err := config.LoadPolicyFile(targetFile)
		if err != nil {
			return fmt.Errorf("failed to load policy: %w", err)
		}

		engine, err := policy.NewEngine(pol)
		if err != nil {
			return fmt.Errorf("policy engine initialization failed: %w", err)
		}

		var opts []mcp.PipeOption
		if toolPrefix != "" {
			opts = append(opts, mcp.WithToolPrefix(toolPrefix))
		}

		cliApprover := approval.NewCLIProvider(os.Stdin, os.Stderr)
		opts = append(opts, mcp.WithApprovalProvider(cliApprover))

		if auditPath != "" {
			recorder, err := audit.NewFileRecorder(auditPath)
			if err != nil {
				return fmt.Errorf("failed to initialize audit log: %w", err)
			}
			defer recorder.Close()
			opts = append(opts, mcp.WithAuditRecorder(recorder))
		}

		pipe := mcp.NewPipe(engine, opts...)
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return pipe.WrapSubprocess(ctx, os.Stdin, os.Stdout, args[0], args[1:]...)
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP reverse proxy daemon for outbound agent API calls",
	RunE: func(cmd *cobra.Command, args []string) error {
		if targetURL == "" {
			return fmt.Errorf("--target URL is required")
		}

		target, err := url.Parse(targetURL)
		if err != nil {
			return fmt.Errorf("invalid target URL: %w", err)
		}

		targetFile := policyPath
		if targetFile == "" {
			targetFile = "circuit.yaml"
		}

		pol, err := config.LoadPolicyFile(targetFile)
		if err != nil {
			return fmt.Errorf("failed to load policy: %w", err)
		}

		engine, err := policy.NewEngine(pol)
		if err != nil {
			return fmt.Errorf("policy engine initialization failed: %w", err)
		}

		var opts []proxy.HandlerOption
		if injectToken != "" {
			opts = append(opts, proxy.WithInjectedToken(injectToken))
		}

		if auditPath != "" {
			recorder, err := audit.NewFileRecorder(auditPath)
			if err != nil {
				return fmt.Errorf("failed to initialize audit log: %w", err)
			}
			defer recorder.Close()
			opts = append(opts, proxy.WithAuditRecorder(recorder))
		}

		cliApprover := approval.NewCLIProvider(os.Stdin, os.Stderr)
		opts = append(opts, proxy.WithApprovalProvider(cliApprover))

		handler := proxy.NewHandler(engine, target, opts...)

		addr := fmt.Sprintf(":%d", port)
		server := &http.Server{
			Addr:    addr,
			Handler: handler,
		}

		fmt.Printf("⚡ Circuit proxy listening on http://localhost%s -> %s\n", addr, targetURL)
		return server.ListenAndServe()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(checkCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(serveCmd)

	mcpCmd.AddCommand(mcpWrapCmd)

	checkCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file")

	runCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file (defaults to circuit.yaml)")
	runCmd.Flags().StringVarP(&targetURL, "target", "t", "", "Target upstream base URL")
	runCmd.Flags().StringVar(&auditPath, "audit", "", "Path to audit log file")
	runCmd.Flags().StringVar(&injectToken, "inject-token", "", "Real token to inject into Authorization header")

	mcpWrapCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file (defaults to circuit.yaml)")
	mcpWrapCmd.Flags().StringVar(&auditPath, "audit", "", "Path to audit log file")
	mcpWrapCmd.Flags().StringVar(&toolPrefix, "prefix", "", "Optional tool prefix namespace")

	serveCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file (defaults to circuit.yaml)")
	serveCmd.Flags().StringVarP(&targetURL, "target", "t", "", "Target upstream base URL (required)")
	serveCmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	serveCmd.Flags().StringVar(&auditPath, "audit", "", "Path to audit log file")
	serveCmd.Flags().StringVar(&injectToken, "inject-token", "", "Real token to inject into Authorization header")
	_ = serveCmd.MarkFlagRequired("target")
}
