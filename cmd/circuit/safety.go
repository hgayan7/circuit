package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/interceptor/forward"
	"github.com/hgayan7/circuit/pkg/policy"
	"github.com/hgayan7/circuit/pkg/safety"
	"github.com/spf13/cobra"
)

func init() {
	var certPath, keyPath, proxyPolicy, listen string
	proxyCmd := &cobra.Command{Use: "proxy", Short: "Inspect HTTP and HTTPS through a local forward proxy", RunE: func(cmd *cobra.Command, args []string) error {
		pol, err := config.LoadPolicyFile(proxyPolicy)
		if err != nil {
			return err
		}
		engine, err := policy.NewEngine(pol)
		if err != nil {
			return err
		}
		var ca *forward.CA
		if certPath != "" || keyPath != "" {
			if certPath == "" || keyPath == "" {
				return fmt.Errorf("both --ca-cert and --ca-key are required")
			}
			ca, err = forward.LoadCA(certPath, keyPath)
		} else {
			ca, err = forward.NewCA()
		}
		if err != nil {
			return err
		}
		dir, err := os.MkdirTemp("", "circuit-proxy-ca-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		bundle, err := ca.WriteBundle(dir)
		if err != nil {
			return err
		}
		cmd.PrintErrf("Circuit proxy: http://%s\nClient CA bundle: %s\n", listen, bundle)
		handler, err := forward.NewHandler(engine, ca)
		if err != nil {
			return err
		}
		server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		go func() {
			<-ctx.Done()
			shutdown, c := context.WithTimeout(context.Background(), 2*time.Second)
			defer c()
			server.Shutdown(shutdown)
		}()
		err = server.ListenAndServe()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}}
	proxyCmd.Flags().StringVar(&proxyPolicy, "policy", "circuit.yaml", "Policy file")
	proxyCmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "Listen address (loopback by default)")
	proxyCmd.Flags().StringVar(&certPath, "ca-cert", "", "Existing CA PEM certificate")
	proxyCmd.Flags().StringVar(&keyPath, "ca-key", "", "Existing CA PEM private key")
	rootCmd.AddCommand(proxyCmd)

	var kind string
	inspectCmd := &cobra.Command{Use: "inspect", Short: "Check stdin for prompt injection, shell risk, or SQL risk", RunE: func(cmd *cobra.Command, args []string) error {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), safety.MaxInspectionBytes+1))
		if err != nil {
			return err
		}
		if len(data) > safety.MaxInspectionBytes {
			return fmt.Errorf("input exceeds 2 MiB limit")
		}
		var finding *safety.Finding
		switch kind {
		case "prompt":
			finding = safety.New(config.SafetyConfig{PromptInjection: true}).Text(string(data))
		case "shell":
			if err := safety.CheckShell(string(data), nil); err != nil {
				finding = &safety.Finding{Kind: "shell", Reason: err.Error()}
			}
		case "sql":
			if err := safety.CheckSQL(string(data), nil); err != nil {
				finding = &safety.Finding{Kind: "sql", Reason: err.Error()}
			}
		default:
			return fmt.Errorf("--kind must be prompt, shell, or sql")
		}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"allowed": finding == nil, "finding": finding}); err != nil {
			return err
		}
		if finding != nil {
			return fmt.Errorf("blocked: %s", finding.Reason)
		}
		return nil
	}}
	inspectCmd.Flags().StringVar(&kind, "kind", "prompt", "Inspection kind: prompt, shell, sql")
	rootCmd.AddCommand(inspectCmd)

	var execPolicy string
	execCmd := &cobra.Command{Use: "exec -- <command> [args...]", Short: "Execute an allowlisted literal command after policy checks", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		pol, err := config.LoadPolicyFile(execPolicy)
		if err != nil {
			return err
		}
		if !pol.Safety.Shell {
			return fmt.Errorf("circuit exec requires safety.shell: true")
		}
		engine, err := policy.NewEngine(pol)
		if err != nil {
			return err
		}
		words := make([]string, len(args))
		for j, arg := range args {
			words[j] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
		result, err := engine.Evaluate(cmd.Context(), &policy.EvaluationContext{Tool: "shell.exec", Args: map[string]any{"command": strings.Join(words, " ")}})
		if err != nil {
			return err
		}
		if result.Action != config.ActionAllow {
			return fmt.Errorf("execution blocked: %s (action %s)", result.Reason, result.Action)
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		child := exec.CommandContext(ctx, args[0], args[1:]...)
		child.Stdin = cmd.InOrStdin()
		child.Stdout = cmd.OutOrStdout()
		child.Stderr = cmd.ErrOrStderr()
		return child.Run()
	}}
	execCmd.Flags().StringVar(&execPolicy, "policy", "circuit.yaml", "Policy file")
	rootCmd.AddCommand(execCmd)
}
