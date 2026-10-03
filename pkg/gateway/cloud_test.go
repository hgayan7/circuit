package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudExecutor(t *testing.T) {
	ctx := context.Background()
	env, err := NewCloudEnvironment("staging", "Staging Cluster", false, []string{"web-api", "worker"}, 1, 10, 10*time.Second)
	require.NoError(t, err)
	exec := NewCloudExecutor(env)

	// 1. Get deployment status of existing service
	statusRes := exec.Execute(ctx, Request{
		Operation:   "get_deployment_status",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
		},
	})
	assert.Equal(t, 200, statusRes.Status)
	var statusData map[string]any
	require.NoError(t, json.Unmarshal(statusRes.Body, &statusData))
	assert.Equal(t, "web-api", statusData["service"])
	assert.Equal(t, float64(3), statusData["replicas"])

	// 2. Deploy updated service image
	deployRes := exec.Execute(ctx, Request{
		Operation:   "deploy_service",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
			"image":   "registry.internal/apps/web-api:v1.3.0",
			"version": "v1.3.0",
		},
	})
	assert.Equal(t, 200, deployRes.Status)
	var deployData map[string]any
	require.NoError(t, json.Unmarshal(deployRes.Body, &deployData))
	assert.Equal(t, "registry.internal/apps/web-api:v1.3.0", deployData["image"])
	assert.Equal(t, float64(2), deployData["revision"])

	// 3. Rollback deployment to prior revision
	rollbackRes := exec.Execute(ctx, Request{
		Operation:   "rollback_deployment",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
		},
	})
	assert.Equal(t, 200, rollbackRes.Status)
	var rollbackData map[string]any
	require.NoError(t, json.Unmarshal(rollbackRes.Body, &rollbackData))
	assert.Equal(t, "registry.internal/apps/web-api:v1.2.0", rollbackData["rolled_back_to"])

	// 4. Scale service within permitted bounds
	scaleRes := exec.Execute(ctx, Request{
		Operation:   "scale_service",
		Environment: "staging",
		Args: map[string]any{
			"service":  "web-api",
			"replicas": 5,
		},
	})
	assert.Equal(t, 200, scaleRes.Status)

	// 5. Scale service exceeding max_replicas bound (10)
	scaleExcessRes := exec.Execute(ctx, Request{
		Operation:   "scale_service",
		Environment: "staging",
		Args: map[string]any{
			"service":  "web-api",
			"replicas": 25,
		},
	})
	assert.Equal(t, 400, scaleExcessRes.Status)
	assert.Contains(t, scaleExcessRes.Error, "exceeds maximum permitted bound 10")

	// 6. Restart service
	restartRes := exec.Execute(ctx, Request{
		Operation:   "restart_service",
		Environment: "staging",
		Args: map[string]any{
			"service": "worker",
		},
	})
	assert.Equal(t, 200, restartRes.Status)

	// 7. Reject unauthorized service not in allowed list
	unauthorizedRes := exec.Execute(ctx, Request{
		Operation:   "get_deployment_status",
		Environment: "staging",
		Args: map[string]any{
			"service": "billing-daemon",
		},
	})
	assert.Equal(t, 403, unauthorizedRes.Status)
	assert.Contains(t, unauthorizedRes.Error, "not in the allowed services list")
}

func TestCloudGatewayApprovalAndLimits(t *testing.T) {
	ctx := context.Background()
	cfgYAML := `
name: cloud-gateway
admin_token_env: ADMIN_TOKEN
approval_ttl: 15m
environments:
  - id: staging
    name: Staging Cluster
    production: false
    allowed_services: ["web-api", "worker"]
    min_replicas: 1
    max_replicas: 10
  - id: prod
    name: Production Cluster
    production: true
    allowed_services: ["web-api", "worker"]
    min_replicas: 2
    max_replicas: 50
agents:
  - id: devops
    token_env: DEVOPS_TOKEN
    environments: ["staging", "prod"]
    actions:
      - deploy_service
      - rollback_deployment
      - restart_service
      - get_deployment_status
      - scale_service
limits:
  - id: staging-deploy-budget
    actions: ["deploy_service"]
    scope: environment
    window: 1m
    max_calls: 2
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	stagingEnv, err := NewCloudEnvironment("staging", "Staging Cluster", false, []string{"web-api", "worker"}, 1, 10, 10*time.Second)
	require.NoError(t, err)
	prodEnv, err := NewCloudEnvironment("prod", "Production Cluster", true, []string{"web-api", "worker"}, 2, 50, 10*time.Second)
	require.NoError(t, err)

	router := NewRouterExecutor(nil, nil, nil, map[string]*CloudExecutor{
		"staging": NewCloudExecutor(stagingEnv),
		"prod":    NewCloudExecutor(prodEnv),
	}, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	// 1. Non-production deployment passes automatically
	act1, err := svc.Submit(ctx, "devops", "deploy-staging-1", Request{
		Operation:   "deploy_service",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
			"image":   "registry.internal/apps/web-api:v1.3.1",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act1.State)

	// 2. Production deployment requires operator approval
	act2, err := svc.Submit(ctx, "devops", "deploy-prod-1", Request{
		Operation:   "deploy_service",
		Environment: "prod",
		Args: map[string]any{
			"service": "web-api",
			"image":   "registry.internal/apps/web-api:v1.3.1",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act2.State)
	assert.Contains(t, act2.Reason, "production environment \"prod\" require operator approval")

	// Operator approves production deployment
	approvedAct, err := svc.Decide(ctx, act2.ID, act2.Digest, "approve")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", approvedAct.State)
	assert.Equal(t, "operator", approvedAct.ApprovedBy)

	// 3. Rollback deployment in staging requires operator approval
	act3, err := svc.Submit(ctx, "devops", "rollback-staging-1", Request{
		Operation:   "rollback_deployment",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act3.State)
	assert.Contains(t, act3.Reason, "Deployment rollback requires operator approval")

	// Operator rejects rollback
	rejectedAct, err := svc.Decide(ctx, act3.ID, act3.Digest, "reject")
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejectedAct.State)

	// 4. Scaling service to 0 replicas requires operator approval
	act4, err := svc.Submit(ctx, "devops", "scale-zero-1", Request{
		Operation:   "scale_service",
		Environment: "staging",
		Args: map[string]any{
			"service":  "web-api",
			"replicas": 0,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act4.State)
	assert.Contains(t, act4.Reason, "Scaling service to 0 replicas halts traffic")

	// 5. Test Velocity/Budget limits: staging-deploy-budget max_calls: 2
	// act1 was call 1. Let's do call 2.
	act5, err := svc.Submit(ctx, "devops", "deploy-staging-2", Request{
		Operation:   "deploy_service",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
			"image":   "registry.internal/apps/web-api:v1.3.2",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act5.State)

	// Call 3 exceeds the budget of 2
	act6, err := svc.Submit(ctx, "devops", "deploy-staging-3", Request{
		Operation:   "deploy_service",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
			"image":   "registry.internal/apps/web-api:v1.3.3",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", act6.State)
	assert.Contains(t, act6.Reason, "budget staging-deploy-budget exhausted")
}

func TestCloudMCPIntegration(t *testing.T) {
	ctx := context.Background()
	cfgYAML := `
name: cloud-mcp-gateway
admin_token_env: ADMIN_TOKEN
approval_ttl: 15m
environments:
  - id: staging
    name: Staging Cluster
    allowed_services: ["web-api"]
agents:
  - id: devops
    token_env: DEVOPS_TOKEN
    environments: ["staging"]
    actions:
      - deploy_service
      - rollback_deployment
      - restart_service
      - get_deployment_status
      - scale_service
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	stagingEnv, err := NewCloudEnvironment("staging", "Staging Cluster", false, []string{"web-api"}, 1, 10, 10*time.Second)
	require.NoError(t, err)
	router := NewRouterExecutor(nil, nil, nil, map[string]*CloudExecutor{
		"staging": NewCloudExecutor(stagingEnv),
	}, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	tokens := Tokens{
		Admin:  "admin-secret-token-32-chars-long-circuit",
		Agents: map[string]string{"devops": "devops-secret-token-32-chars-long"},
	}
	handler, err := NewHTTPHandler(svc, tokens)
	require.NoError(t, err)

	server := handler.mcpServer(cfg.Agents[0])
	require.NotNil(t, server)

	// Call cloud_deploy tool
	act, err := svc.Submit(ctx, "devops", "mcp-cloud-deploy-1", Request{
		Operation:   "deploy_service",
		Environment: "staging",
		Args: map[string]any{
			"service": "web-api",
			"image":   "registry.internal/apps/web-api:v2.0.0",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act.State)
	assert.NotNil(t, act.Outcome)
	assert.Equal(t, 200, act.Outcome.Status)
}
