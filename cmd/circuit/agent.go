package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hgayan7/circuit/pkg/onboarding"
	"github.com/spf13/cobra"
)

func init() {
	agent := &cobra.Command{Use: "agent", Short: "Run isolated agents with gateway-only egress, or use a legacy restricted runner"}
	agent.AddCommand(newAgentRunCommand())
	rootCmd.AddCommand(agent)
}
func newAgentRunCommand() *cobra.Command {
	var o onboarding.SandboxOptions
	var dryRun bool
	var dir, boundaryImage string
	cmd := &cobra.Command{Use: "run [flags] -- command [args...]", Short: "Run an agent in a restricted Docker container on an existing internal gateway network", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) (result error) {
		o.Command = args
		var deployment *onboarding.Deployment
		if dir != "" {
			s, err := onboarding.Load(dir)
			if err != nil {
				return err
			}
			deployment, err = onboarding.LoadDeployment(dir)
			if err != nil {
				return err
			}
			if err := onboarding.CheckWorkspaceIsolation(s, o.Workspace); err != nil {
				return err
			}
			o.Connection, o.Network, o.GatewayURL = s.Connection, deployment.Network, "https://gateway:8443"
			if o.Writable {
				return fmt.Errorf("isolated deployment disallows writable host mounts")
			}
			if o.Runtime != "" {
				return fmt.Errorf("isolated deployment uses the qualified runc network path; alternative runtimes require separate validation")
			}
		}
		if _, err := onboarding.SandboxArgs(o, "/private/staged-agent-files"); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		preflight, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := onboarding.CheckInternalNetwork(preflight, o.Network); err != nil {
			return err
		}
		tools, err := onboarding.Verify(preflight, o.Connection)
		if err != nil {
			return fmt.Errorf("agent credential preflight failed: %w", err)
		}
		if dryRun {
			plan, err := onboarding.SandboxArgs(o, "/private/staged-agent-files")
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(map[string]any{"docker_args": plan, "permitted_tools": tools, "verified": "host-side agent identity and internal-network configuration only; container reachability and isolation still require tests"}, "", "  ")
			cmd.Println(string(data))
			return nil
		}
		staged, err := onboarding.StageSandbox(o)
		if err != nil {
			return err
		}
		defer os.RemoveAll(staged)
		plan, err := onboarding.SandboxArgs(o, staged)
		if err != nil {
			return err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		name := "circuit-agent-" + hex.EncodeToString(random[:])
		boundary := name + "-boundary"
		plan = append([]string{plan[0], "--name", name}, plan[1:]...)
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			output, err := exec.CommandContext(cleanup, "docker", "rm", "--force", name).CombinedOutput()
			if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such container") {
				result = fmt.Errorf("sandbox cleanup could not be confirmed; remove container %s before discarding its scoped credential", name)
			}
			if deployment != nil {
				output, err = exec.CommandContext(cleanup, "docker", "rm", "--force", boundary).CombinedOutput()
				if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such container") {
					result = fmt.Errorf("network boundary cleanup failed; remove container %s", boundary)
				}
			}
		}()
		if deployment != nil {
			ip, err := onboarding.GatewayAddress(ctx, deployment)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(staged, "hosts"), []byte("127.0.0.1 localhost\n"+ip+" gateway\n"), 0444); err != nil {
				return err
			}
			if err := onboarding.StartBoundary(ctx, deployment, boundaryImage, boundary, ip); err != nil {
				return err
			}
			plan, err = onboarding.NamespacedArgs(o, staged, boundary)
			if err != nil {
				return err
			}
			plan = append([]string{plan[0], "--name", name}, plan[1:]...)
		}
		process := exec.CommandContext(ctx, "docker", plan...)
		process.Stdin = cmd.InOrStdin()
		process.Stdout = cmd.OutOrStdout()
		process.Stderr = cmd.ErrOrStderr()
		if deployment != nil {
			cmd.PrintErrln("Isolated agent: only gateway TCP/8443 allowed; IPv6, DNS, public and host-network egress denied. Model and tool calls must use declared Circuit routes. No provider/operator credentials mounted.")
		} else {
			cmd.PrintErrln("Legacy restricted runner: internal Docker network only, no per-agent firewall. Use --dir with circuit up for enforced gateway-only egress.")
		}
		if err := process.Run(); err != nil {
			return fmt.Errorf("sandbox command failed or was interrupted; inspect agent output and gateway action history")
		}
		return nil
	}}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "Use a circuit up deployment; automatically configure credentials/network and enforce gateway-only egress")
	f.StringVar(&boundaryImage, "boundary-image", "circuit-boundary:local", "Trusted firewall image built from deploy/docker/Dockerfile target boundary")
	f.StringVar(&o.Image, "image", "", "Trusted agent image; pin a digest for reproducible deployment")
	f.StringVar(&o.Network, "network", "", "Existing internal Docker network shared with the gateway")
	f.StringVar(&o.Workspace, "workspace", "", "Explicit workspace directory; mounted read-only by default")
	f.BoolVar(&o.Writable, "writable", false, "Allow local workspace writes; this does not require gateway approval")
	f.StringVar(&o.Connection.URL, "url", "", "Host-reachable gateway HTTPS origin for agent-role preflight")
	f.StringVar(&o.GatewayURL, "gateway-url", "https://gateway:8443", "Gateway HTTPS origin inside the internal Docker network")
	f.StringVar(&o.Connection.TokenFile, "token-file", "", "Private agent-only credential file outside the workspace")
	f.StringVar(&o.Connection.CACert, "ca-cert", "", "Public gateway CA file outside the workspace")
	f.StringVar(&o.Runtime, "runtime", "", "Optional runsc runtime; requires separately installed gVisor")
	f.BoolVar(&dryRun, "dry-run", false, "Check identity/network and show restricted plan without launching an agent")
	return cmd
}
