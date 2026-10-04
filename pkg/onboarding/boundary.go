package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"time"
)

func GatewayAddress(ctx context.Context, d *Deployment) (string, error) {
	if err := CheckInternalNetwork(ctx, d.Network); err != nil {
		return "", err
	}
	data, err := exec.CommandContext(ctx, "docker", "inspect", d.Gateway).Output()
	if err != nil {
		return "", fmt.Errorf("deployed gateway unavailable; run circuit up")
	}
	var containers []struct {
		Config          struct{ Labels map[string]string }
		State           struct{ Running bool }
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if json.Unmarshal(data, &containers) != nil || len(containers) != 1 {
		return "", fmt.Errorf("invalid gateway inspection")
	}
	c := containers[0]
	ip := c.NetworkSettings.Networks[d.Network].IPAddress
	if !c.State.Running || c.Config.Labels["circuit.project"] != d.Project || c.Config.Labels["circuit.role"] != "gateway" || net.ParseIP(ip).To4() == nil {
		return "", fmt.Errorf("gateway must be the running deployment-owned container on its private IPv4 network")
	}
	return ip, nil
}

func BoundaryArgs(d *Deployment, image, name, ip string) ([]string, error) {
	if !validImage(image) || net.ParseIP(ip).To4() == nil || name == "" {
		return nil, fmt.Errorf("boundary requires a trusted image and verified gateway IPv4 address")
	}
	return []string{"run", "-d", "--runtime", "runc", "--name", name, "--label", "circuit.project=" + d.Project, "--label", "circuit.role=boundary", "--network", d.Network,
		"--read-only", "--cap-drop", "ALL", "--cap-add", "NET_ADMIN", "--security-opt", "no-new-privileges", "--pids-limit", "8", "--memory", "32m", "--cpus", "0.1", "--tmpfs", "/tmp:rw,noexec,nosuid,size=1m", "--dns", "127.0.0.1", "--env", "CIRCUIT_GATEWAY_IP=" + ip,
		"--entrypoint", "/bin/sh", image, "/usr/local/bin/circuit-boundary"}, nil
}

func StartBoundary(ctx context.Context, d *Deployment, image, name, ip string) error {
	args, err := BoundaryArgs(d, image, name, ip)
	if err != nil {
		return err
	}
	if _, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("network boundary failed to start; ensure the boundary image is built")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if exec.CommandContext(ctx, "docker", "exec", name, "test", "-f", "/tmp/ready").Run() == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("network firewall never became ready; agent was not launched (check docker logs %s)", name)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// NamespacedArgs reuses the restricted spec but removes the agent's independent network.
func NamespacedArgs(o SandboxOptions, staged, boundary string) ([]string, error) {
	if o.Runtime != "" {
		return nil, fmt.Errorf("gateway-only firewall mode is qualified with runc only; alternative runtime packet paths need separate validation")
	}
	if o.Writable {
		return nil, fmt.Errorf("isolated deployment rejects writable host workspaces; use governed writes")
	}
	args, err := SandboxArgs(o, staged)
	if err != nil {
		return nil, err
	}
	for i := range args {
		if args[i] == "--network" {
			args[i+1] = "container:" + boundary
			break
		}
	}
	// A static hosts mount avoids granting DNS egress. No host/provider paths are added.
	args = append(args[:1], append([]string{"--runtime", "runc", "--mount", "type=bind,src=" + staged + "/hosts,dst=/etc/hosts,readonly"}, args[1:]...)...)
	return args, nil
}
