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
	"github.com/hgayan7/circuit/pkg/initwizard"
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
	Short: "Circuit — bounded GitHub actions for autonomous agents",
	Long: `Circuit gives agents scoped GitHub access with durable action limits,
exact-action approvals, and an execution history.

Start with a local simulation:
  circuit gateway demo

Configure a GitHub workflow:
  circuit gateway init --repo owner/repository
  circuit gateway serve

Additional inspection tools:
  circuit run --policy circuit.yaml -- python agent.py
  circuit mcp wrap -- npx -y @my/mcp-server
  circuit serve --target https://api.openai.com`,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a circuit.yaml policy file interactively",
	Long: `Asks a few questions about your agent stack and generates
a tailored circuit.yaml with sensible default rules, budgets, and audit settings.

  circuit init`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return initwizard.Run(os.Stdin, cmd.OutOrStdout(), cwd)
	},
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
	Short:   "Validate circuit.yaml and compile all CEL rules",
	Long: `Parses your circuit.yaml, compiles every CEL rule expression,
and reports any errors before you run your agent.

  circuit check                  # validates ./circuit.yaml
  circuit check path/to/policy.yaml`,
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
	Use:   "run -- <command> [args...]",
	Short: "Wrap an agent process — intercept every outbound HTTP call",
	Long: `Starts an ephemeral proxy, injects HTTP_PROXY into your agent's environment,
and enforces your circuit.yaml policies on every outbound request.
No changes to your agent code required.

  circuit run -- python agent.py
  circuit run --audit agent.ndjson -- node agent.js
  circuit run --policy strict.yaml -- go run ./agent`,
	Args: cobra.MinimumNArgs(1),
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
	Short: "Wrap and govern MCP stdio servers",
	Long: `Commands for intercepting Model Context Protocol (MCP) tool calls.

  circuit mcp wrap -- npx -y @modelcontextprotocol/server-postgres
  circuit mcp wrap -- uvx mcp-server-git`,
}

var mcpWrapCmd = &cobra.Command{
	Use:   "wrap -- <command> [args...]",
	Short: "Sit between your LLM and an MCP server — enforce policies on every tool call",
	Long: `Spawns the MCP server as a subprocess and acts as a stdio man-in-the-middle.
Every tools/call JSON-RPC message is evaluated against your circuit.yaml before
being forwarded. Works with Claude Desktop, Cursor, and any MCP-compatible host.

  circuit mcp wrap -- npx -y @modelcontextprotocol/server-postgres
  circuit mcp wrap --audit mcp.ndjson -- uvx mcp-server-git
  circuit mcp wrap --policy strict.yaml -- python my_mcp_server.py`,
	Args: cobra.MinimumNArgs(1),
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

		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			defer tty.Close()
			opts = append(opts, mcp.WithApprovalProvider(approval.NewCLIProvider(tty, tty)))
		}

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
	Use:   "serve --target <url>",
	Short: "Run as a persistent proxy daemon — ideal for Docker/k8s sidecars",
	Long: `Starts a long-running HTTP reverse proxy that enforces your circuit.yaml
on every request forwarded to the upstream target. Useful when you can't
wrap the agent process directly (containers, remote agents, shared proxies).

  circuit serve --target https://api.openai.com
  circuit serve --target https://api.openai.com --port 9090 --audit audit.ndjson`,
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
	rootCmd.AddCommand(initCmd)
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
