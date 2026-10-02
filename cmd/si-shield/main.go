package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/himshikhargayan/si-shield/pkg/approval"
	"github.com/himshikhargayan/si-shield/pkg/audit"
	"github.com/himshikhargayan/si-shield/pkg/config"
	"github.com/himshikhargayan/si-shield/pkg/interceptor/mcp"
	"github.com/himshikhargayan/si-shield/pkg/interceptor/proxy"
	"github.com/himshikhargayan/si-shield/pkg/policy"
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
	Use:   "si-shield",
	Short: "Autonomous Agent Security & Governance Gateway",
	Long: `si-shield is a high-performance, zero-trust security sidecar and gateway 
designed for AI agents and Model Context Protocol (MCP) tooling. 
It intercepts outbound tool calls and APIs, enforcing argument-level policies 
and cumulative session budgets.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the si-shield version",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Printf("si-shield v%s\n", version)
	},
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate and compile a policy YAML file",
	RunE: func(cmd *cobra.Command, args []string) error {
		pol, err := config.LoadPolicyFile(policyPath)
		if err != nil {
			return fmt.Errorf("failed to load policy: %w", err)
		}

		engine, err := policy.NewEngine(pol)
		if err != nil {
			return fmt.Errorf("policy compilation failed: %w", err)
		}

		_ = engine
		cmd.Printf("Policy '%s' (version %s) compiled successfully with %d rule(s).\n",
			pol.Name, pol.Version, len(pol.Rules))
		return nil
	},
}

var wrapCmd = &cobra.Command{
	Use:   "wrap --policy <file> -- <command> [args...]",
	Short: "Wrap an MCP server stdio process with policy protection",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pol, err := config.LoadPolicyFile(policyPath)
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

		// Configure CLI HITL approver
		cliApprover := approval.NewCLIProvider(os.Stdin, os.Stderr)
		opts = append(opts, mcp.WithApprovalProvider(cliApprover))

		// Configure Audit recorder
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

		subCmd := args[0]
		subArgs := args[1:]

		return pipe.WrapSubprocess(ctx, os.Stdin, os.Stdout, subCmd, subArgs...)
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP reverse proxy for outbound agent API calls",
	RunE: func(cmd *cobra.Command, args []string) error {
		if targetURL == "" {
			return fmt.Errorf("--target URL is required")
		}

		target, err := url.Parse(targetURL)
		if err != nil {
			return fmt.Errorf("invalid target URL: %w", err)
		}

		pol, err := config.LoadPolicyFile(policyPath)
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

		fmt.Printf("🛡️  si-shield proxy listening on http://localhost%s -> %s\n", addr, targetURL)
		return server.ListenAndServe()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(wrapCmd)
	rootCmd.AddCommand(serveCmd)

	validateCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file (required)")
	_ = validateCmd.MarkFlagRequired("policy")

	wrapCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file (required)")
	wrapCmd.Flags().StringVar(&auditPath, "audit", "", "Path to audit log file")
	wrapCmd.Flags().StringVar(&toolPrefix, "prefix", "", "Optional tool prefix namespace")
	_ = wrapCmd.MarkFlagRequired("policy")

	serveCmd.Flags().StringVarP(&policyPath, "policy", "p", "", "Path to policy YAML file (required)")
	serveCmd.Flags().StringVarP(&targetURL, "target", "t", "", "Target upstream base URL (required)")
	serveCmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	serveCmd.Flags().StringVar(&auditPath, "audit", "", "Path to audit log file")
	serveCmd.Flags().StringVar(&injectToken, "inject-token", "", "Real token to inject into Authorization header")
	_ = serveCmd.MarkFlagRequired("policy")
	_ = serveCmd.MarkFlagRequired("target")
}
