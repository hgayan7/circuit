package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/hgayan7/circuit/pkg/onboarding"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newSetupCommand(), newStartCommand(), newConnectCommand(), newDoctorCommand())
}
func newSetupCommand() *cobra.Command {
	o := onboarding.Options{}
	var demo, nonInteractive bool
	cmd := &cobra.Command{Use: "setup", Short: "Guided BYOK setup and agent connection", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		reader := bufio.NewReader(cmd.InOrStdin())
		ask := func(label, defaultValue string) (string, error) {
			cmd.Printf("%s [%s]: ", label, defaultValue)
			line, err := reader.ReadString('\n')
			if err != nil && strings.TrimSpace(line) == "" {
				return "", fmt.Errorf("input ended; use --non-interactive with explicit integration flags")
			}
			line = strings.TrimSpace(line)
			if line == "" {
				line = defaultValue
			}
			return line, nil
		}
		if demo {
			o.Integration = "demo"
		}
		if o.Integration == "" && !nonInteractive {
			value, err := ask("Choose demo, github, postgres, workspace, plugin, or middleware", "demo")
			if err != nil {
				return err
			}
			o.Integration = value
		}
		if o.Integration == "demo" {
			port := o.Port
			if !cmd.Flags().Changed("port") {
				port = 8080
			}
			if port < 1 || port > 65535 {
				return fmt.Errorf("invalid port")
			}
			demoCmd, _, err := rootCmd.Find([]string{"gateway", "demo"})
			if err != nil {
				return err
			}
			previous := demoCmd.Flags().Lookup("listen").Value.String()
			defer demoCmd.Flags().Set("listen", previous)
			if err := demoCmd.Flags().Set("listen", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
				return err
			}
			return demoCmd.RunE(cmd, nil)
		}
		if !nonInteractive {
			fields := []struct {
				label    string
				value    *string
				fallback string
			}{}
			switch o.Integration {
			case "github":
				cmd.Println("Create/install a GitHub App: https://github.com/settings/apps. Select only intended repositories; keep its PEM outside the agent workspace.")
				fields = append(fields, struct {
					label    string
					value    *string
					fallback string
				}{"Repository (owner/name)", &o.Repository, ""}, struct {
					label    string
					value    *string
					fallback string
				}{"Private-key PEM path (not its contents)", &o.KeyFile, ""})
				if o.AppID == 0 {
					value, err := ask("App ID", "")
					if err != nil {
						return err
					}
					o.AppID, err = strconv.ParseInt(value, 10, 64)
					if err != nil {
						return fmt.Errorf("App ID must be a positive integer")
					}
				}
				if o.InstallationID == 0 {
					value, err := ask("Installation ID", "")
					if err != nil {
						return err
					}
					o.InstallationID, err = strconv.ParseInt(value, 10, 64)
					if err != nil {
						return fmt.Errorf("installation ID must be a positive integer")
					}
				}
			case "postgres":
				fields = append(fields, struct {
					label    string
					value    *string
					fallback string
				}{"Database credential environment-variable name (not the DSN)", &o.DSNEnv, "CIRCUIT_DATABASE_DSN"})
			case "workspace":
				fields = append(fields, struct {
					label    string
					value    *string
					fallback string
				}{"Read-only workspace path", &o.Workspace, ""})
			case "middleware":
				fields = append(fields, struct {
					label    string
					value    *string
					fallback string
				}{"Reviewed upstream manifest path", &o.UpstreamManifest, ""})
				cmd.Println("Discovery grants no permissions. Use review-writes unless you have explicitly classified safe read-only operations in the manifest.")
			case "plugin":
				fields = append(fields, struct {
					label    string
					value    *string
					fallback string
				}{"Plugin endpoint", &o.Endpoint, ""}, struct {
					label    string
					value    *string
					fallback string
				}{"Private plugin-token file", &o.PluginTokenFile, ""})
			default:
				return fmt.Errorf("choose demo, github, postgres, workspace, plugin, or middleware")
			}
			for _, field := range fields {
				if *field.value == "" {
					value, err := ask(field.label, field.fallback)
					if err != nil {
						return err
					}
					*field.value = value
				}
			}
			if o.Integration == "plugin" && len(o.PluginOperations) == 0 {
				value, err := ask("Declared operations (comma-separated)", "")
				if err != nil {
					return err
				}
				o.PluginOperations = splitList(value)
			}
			if o.Integration == "plugin" && len(o.PluginReadOnly) == 0 {
				value, err := ask("Read-only operations (comma-separated; only genuinely non-mutating tools)", "")
				if err != nil {
					return err
				}
				o.PluginReadOnly = splitList(value)
			}
			if !cmd.Flags().Changed("preset") {
				label := "Preset: read-only or review-writes"
				if o.Integration == "github" {
					label = "Preset: read-only, pr-author, or pr-author-with-merge"
				}
				value, err := ask(label, "read-only")
				if err != nil {
					return err
				}
				o.Preset = value
			}
		}
		dir, err := filepath.Abs(o.Directory)
		if err != nil {
			return err
		}
		cfg, err := onboarding.BuildConfig(o, dir)
		if err != nil {
			return err
		}
		cmd.Printf("Integration: %s; preset: %s\nAllowed actions: %s\nLimits: 100 actions/hour; write presets require exact approval and allow at most 5 writes/hour.\n", o.Integration, o.Preset, strings.Join(cfg.Agents[0].Actions, ", "))
		if !nonInteractive {
			value, err := ask("Create this setup?", "no")
			if err != nil {
				return err
			}
			if value != "yes" && value != "y" {
				cmd.Println("No files created.")
				return nil
			}
		}
		o.Executable, err = os.Executable()
		if err != nil {
			return err
		}
		s, err := onboarding.Create(o)
		if err != nil {
			return err
		}
		cmd.Printf("Created private setup: %s\nStart: circuit start --dir %q\nVerify after starting: circuit doctor --dir %q\nMCP client configuration: %s\nReview UI: %s\nOperator token file: %s\n", dir, dir, dir, filepath.Join(dir, "mcp.json"), s.Connection.URL, filepath.Join(dir, "secrets", "admin-token"))
		cmd.Println("Local TLS certificate only; browser trust warning is expected. No system trust was changed. Production deployment, provider access, and agent isolation still need verification.")
		cmd.Println("Keep this operator directory and all provider credentials outside agent workspaces and sandbox mounts. Share only the agent token and public CA.")
		return nil
	}}
	f := cmd.Flags()
	f.BoolVar(&demo, "demo", false, "Start a credential-free simulation")
	f.BoolVar(&nonInteractive, "non-interactive", false, "Use explicit flags without prompting")
	f.StringVar(&o.Directory, "out", onboarding.DefaultDirectory(), "New operator-only setup directory outside the agent workspace; never overwrite")
	f.StringVar(&o.Integration, "integration", "", "github, postgres, workspace, plugin, or middleware")
	f.StringVar(&o.UpstreamManifest, "upstream-manifest", "", "Reviewed MCP/REST forwarding target manifest")
	f.StringVar(&o.Preset, "preset", "read-only", "Permission preset; default read-only")
	f.IntVar(&o.Port, "port", 8643, "Loopback gateway port")
	f.StringVar(&o.Repository, "repo", "", "Allowed GitHub owner/repository")
	f.Int64Var(&o.AppID, "app-id", 0, "GitHub App ID")
	f.Int64Var(&o.InstallationID, "installation-id", 0, "GitHub App installation ID")
	f.StringVar(&o.KeyFile, "key-file", "", "Existing private RSA PEM file")
	f.StringVar(&o.DSNEnv, "dsn-env", "", "Gateway-only environment variable containing PostgreSQL DSN")
	f.StringVar(&o.Workspace, "workspace", "", "Existing read-only workspace")
	f.StringVar(&o.Endpoint, "endpoint", "", "Fixed plugin HTTPS endpoint; HTTP only on loopback")
	f.StringVar(&o.PluginTokenFile, "plugin-token-file", "", "Existing private plugin credential file")
	f.StringSliceVar(&o.PluginOperations, "operations", nil, "Declared plugin operations")
	f.StringSliceVar(&o.PluginReadOnly, "read-only-operations", nil, "Verified non-mutating plugin operations")
	return cmd
}
func splitList(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
func newStartCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{Use: "start", Short: "Start a generated gateway setup; does not launch an agent sandbox", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := onboarding.Load(dir)
		if err != nil {
			return err
		}
		cfg, err := gateway.LoadConfig(s.Config)
		if err != nil {
			return err
		}
		if err := onboarding.CheckCredentials(cfg); err != nil {
			return err
		}
		if err := onboarding.CheckTokens(cfg); err != nil {
			return err
		}
		return runGateway(cmd, s.Config, s.State, s.Listen, s.TLSCert, s.TLSKey, s.Integration == "github")
	}}
	cmd.Flags().StringVar(&dir, "dir", onboarding.DefaultDirectory(), "Setup directory")
	return cmd
}
func newConnectCommand() *cobra.Command {
	var c onboarding.Connection
	cmd := &cobra.Command{Use: "connect", Short: "Expose a scoped Circuit gateway to stdio MCP clients", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return onboarding.Bridge(ctx, c, &mcp.StdioTransport{})
	}}
	f := cmd.Flags()
	f.StringVar(&c.URL, "url", "", "Gateway HTTPS origin")
	f.StringVar(&c.TokenFile, "token-file", "", "Agent-only credential file")
	f.BoolVar(&c.MountedToken, "mounted-token", false, "Linux container only: require credential on a read-only filesystem mount")
	f.StringVar(&c.CACert, "ca-cert", "", "Trusted gateway CA; defaults to system trust")
	return cmd
}
func newDoctorCommand() *cobra.Command {
	var dir string
	var offline bool
	cmd := &cobra.Command{Use: "doctor", Short: "Validate setup and check gateway readiness and scoped MCP discovery", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := onboarding.Load(dir)
		if err != nil {
			return err
		}
		cfg, err := gateway.LoadConfig(s.Config)
		if err != nil {
			return err
		}
		if err := onboarding.CheckCredentials(cfg); err != nil {
			return err
		}
		if err := onboarding.CheckTokens(cfg); err != nil {
			return err
		}
		if _, err := onboarding.Client(s.Connection); err != nil {
			return err
		}
		cmd.Println("PASS configuration and credential files (values never printed)")
		if offline {
			cmd.Println("Offline only: provider access, gateway readiness, and agent isolation are not verified.")
			return nil
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		tools, err := onboarding.Verify(ctx, s.Connection)
		if err != nil {
			return err
		}
		cmd.Printf("PASS verified TLS, readiness, and agent-scoped MCP discovery: %s\n", strings.Join(tools, ", "))
		cmd.Println("No tool actions executed. Provider access and approval behavior need a scoped staging rehearsal; sandbox/egress isolation is not verified.")
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", onboarding.DefaultDirectory(), "Setup directory")
	cmd.Flags().BoolVar(&offline, "offline", false, "Validate configuration without connecting")
	return cmd
}
