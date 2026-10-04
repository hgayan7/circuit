package main

import (
	"fmt"
	"os/exec"

	"github.com/hgayan7/circuit/pkg/onboarding"
	"github.com/spf13/cobra"
)

func init() {
	var dir, image string
	up := &cobra.Command{Use: "up", Short: "Provision the isolated Docker gateway and private agent network", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		d, err := onboarding.PrepareDeployment(dir, image)
		if err != nil {
			return err
		}
		process := exec.CommandContext(cmd.Context(), "docker", "compose", "-p", d.Project, "-f", d.Compose, "up", "-d", "--wait", "--wait-timeout", "60")
		process.Stdout, process.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if err := process.Run(); err != nil {
			return fmt.Errorf("isolated gateway startup failed; inspect docker compose logs using %s", d.Compose)
		}
		cmd.Printf("Gateway ready. Run: circuit agent run --dir %q --image YOUR_AGENT_IMAGE --workspace YOUR_WORKSPACE -- YOUR_COMMAND\n", dir)
		cmd.Println("Agent runs use a fail-closed network boundary. Mount only a reviewed workspace; model calls must use a declared Circuit route too.")
		return nil
	}}
	up.Flags().StringVar(&dir, "dir", onboarding.DefaultDirectory(), "Private setup directory")
	up.Flags().StringVar(&image, "gateway-image", "circuit-gateway:local", "Trusted gateway image; build from deploy/docker/Dockerfile or pin your release digest")
	var downDir string
	down := &cobra.Command{Use: "down", Short: "Stop the isolated gateway; retain its durable state volume", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		d, err := onboarding.LoadDeployment(downDir)
		if err != nil {
			return err
		}
		process := exec.CommandContext(cmd.Context(), "docker", "compose", "-p", d.Project, "-f", d.Compose, "down")
		process.Stdout, process.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if err := process.Run(); err != nil {
			return err
		}
		cmd.Println("Gateway stopped; state volume retained. Stop active agent runs before deleting the deployment.")
		return nil
	}}
	down.Flags().StringVar(&downDir, "dir", onboarding.DefaultDirectory(), "Private setup directory")
	rootCmd.AddCommand(up, down)
}
