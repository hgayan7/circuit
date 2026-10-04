package onboarding

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hgayan7/circuit/pkg/gateway"
	"gopkg.in/yaml.v3"
)

func loadUpstreamManifest(path string) (*gateway.CustomToolConfig, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("upstream manifest must be a regular YAML file at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("upstream manifest unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("upstream manifest unavailable or oversized")
	}
	var manifest struct {
		CustomTools []gateway.CustomToolConfig `yaml:"custom_tools"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if decoder.Decode(&manifest) != nil || len(manifest.CustomTools) != 1 {
		return nil, fmt.Errorf("manifest must contain exactly one custom_tools target, not agent or policy configuration")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("manifest must contain exactly one YAML document")
	}
	target := manifest.CustomTools[0]
	if target.Protocol != gateway.MCPForwardProtocol && target.Protocol != gateway.RESTForwardProtocol {
		return nil, fmt.Errorf("manifest must use an MCP or REST forwarding profile")
	}
	if target.TokenFile == "" || target.TokenEnv != "" {
		return nil, fmt.Errorf("guided middleware setup requires a gateway-owned upstream token_file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for _, ref := range []*string{&target.TokenFile, &target.CACert} {
		if *ref != "" && !filepath.IsAbs(*ref) {
			*ref = filepath.Join(filepath.Dir(absolute), *ref)
		}
	}
	return &target, nil
}
