package main

import (
	"fmt"
	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func addGatewayForwarding(gatewayCmd *cobra.Command) {
	var endpoint, tokenFile, caCert string
	cmd := &cobra.Command{Use: "discover-mcp", Short: "Discover MCP tool schemas into a review-only gateway target manifest", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		manifest, err := gateway.DiscoverMCP(cmd.Context(), endpoint, tokenFile, caCert)
		if err != nil {
			return err
		}
		data, err := yaml.Marshal(map[string]any{"custom_tools": []gateway.CustomToolConfig{*manifest}})
		if err != nil {
			return err
		}
		cmd.PrintErrln("Review this manifest before registering it. No agent permissions granted; no read-only annotations trusted; no tools executed.")
		_, err = fmt.Fprint(cmd.OutOrStdout(), string(data))
		return err
	}}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Fixed upstream Streamable HTTP MCP endpoint")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "Gateway-owned upstream credential file; never an agent/operator token")
	cmd.Flags().StringVar(&caCert, "ca-cert", "", "Optional private upstream CA")
	gatewayCmd.AddCommand(cmd)
}
