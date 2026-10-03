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
	"strings"
	"syscall"
	"time"

	"github.com/hgayan7/circuit/pkg/onboarding"
	"github.com/spf13/cobra"
)

func init() {
	agent := &cobra.Command{Use: "agent", Short: "Optional sandbox execution; the gateway also works with your own sandbox"}
	agent.AddCommand(newAgentRunCommand())
	rootCmd.AddCommand(agent)
}
func newAgentRunCommand() *cobra.Command {
	var o onboarding.SandboxOptions
	var dryRun bool
	cmd := &cobra.Command{Use: "run [flags] -- command [args...]", Short: "Run an agent in a restricted Docker container on an existing internal gateway network", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) (result error) {
		o.Command = args
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
		plan = append([]string{plan[0], "--name", name}, plan[1:]...)
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			output, err := exec.CommandContext(cleanup, "docker", "rm", "--force", name).CombinedOutput()
			if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such container") {
				result = fmt.Errorf("sandbox cleanup could not be confirmed; remove container %s before discarding its scoped credential", name)
			}
		}()
		process := exec.CommandContext(ctx, "docker", plan...)
		process.Stdin = cmd.InOrStdin()
		process.Stdout = cmd.OutOrStdout()
		process.Stderr = cmd.ErrOrStderr()
		cmd.PrintErrln("Restricted Docker agent: no provider/operator credentials or Docker socket mounted. Tool calls must use Circuit; model access needs a separately governed broker. Container images must include your agent and the Circuit connector if using the generated MCP configuration.")
		if err := process.Run(); err != nil {
			return fmt.Errorf("sandbox command failed or was interrupted; inspect agent output and gateway action history")
		}
		return nil
	}}
	f := cmd.Flags()
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
