package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// CloudEnvironment defines an isolated cloud deployment target (e.g. cluster, namespace, or environment).
type CloudEnvironment struct {
	ID              string        `yaml:"id" json:"id"`
	Name            string        `yaml:"name" json:"name"`
	Production      bool          `yaml:"production,omitempty" json:"production,omitempty"`
	AllowedServices []string      `yaml:"allowed_services,omitempty" json:"allowed_services,omitempty"`
	MaxReplicas     int           `yaml:"max_replicas,omitempty" json:"max_replicas,omitempty"`
	MinReplicas     int           `yaml:"min_replicas,omitempty" json:"min_replicas,omitempty"`
	MaxTimeout      time.Duration `yaml:"max_timeout,omitempty" json:"max_timeout,omitempty"`

	simulated bool
	simMu     sync.RWMutex
	services  map[string]*SimulatedService
}

// SimulatedService maintains deployment state for testing and demos.
type SimulatedService struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Version      string            `json:"version"`
	Replicas     int               `json:"replicas"`
	Status       string            `json:"status"`
	Revision     int               `json:"revision"`
	PastImages   []string          `json:"past_images"`
	EnvVars      map[string]string `json:"env_vars,omitempty"`
	LastDeployed time.Time         `json:"last_deployed"`
}

// NewCloudEnvironment creates a CloudEnvironment.
func NewCloudEnvironment(id, name string, production bool, allowedServices []string, minReplicas, maxReplicas int, timeout time.Duration) (*CloudEnvironment, error) {
	if id == "" {
		return nil, fmt.Errorf("cloud environment ID is required")
	}
	if maxReplicas <= 0 {
		maxReplicas = 50
	}
	if minReplicas < 0 {
		minReplicas = 0
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	env := &CloudEnvironment{
		ID:              id,
		Name:            name,
		Production:      production,
		AllowedServices: allowedServices,
		MinReplicas:     minReplicas,
		MaxReplicas:     maxReplicas,
		MaxTimeout:      timeout,
		simulated:       true,
		services:        make(map[string]*SimulatedService),
	}

	// Seed default services for testing/simulation
	env.services["web-api"] = &SimulatedService{
		Name:         "web-api",
		Image:        "registry.internal/apps/web-api:v1.2.0",
		Version:      "v1.2.0",
		Replicas:     3,
		Status:       "healthy",
		Revision:     1,
		PastImages:   []string{"registry.internal/apps/web-api:v1.1.9"},
		LastDeployed: time.Now().Add(-24 * time.Hour),
	}
	env.services["worker"] = &SimulatedService{
		Name:         "worker",
		Image:        "registry.internal/apps/worker:v2.0.1",
		Version:      "v2.0.1",
		Replicas:     2,
		Status:       "healthy",
		Revision:     1,
		PastImages:   []string{},
		LastDeployed: time.Now().Add(-12 * time.Hour),
	}

	return env, nil
}

// CloudExecutor executes governed cloud and deployment actions against a CloudEnvironment.
type CloudExecutor struct {
	env *CloudEnvironment
}

// NewCloudExecutor creates a CloudExecutor for an environment.
func NewCloudExecutor(env *CloudEnvironment) *CloudExecutor {
	return &CloudExecutor{env: env}
}

func (e *CloudExecutor) checkServiceAllowed(svc string) error {
	if len(e.env.AllowedServices) == 0 {
		return nil
	}
	for _, allowed := range e.env.AllowedServices {
		if strings.EqualFold(allowed, svc) {
			return nil
		}
	}
	return fmt.Errorf("service %q is not in the allowed services list for environment %s", svc, e.env.ID)
}

func (e *CloudExecutor) Execute(ctx context.Context, r Request) Outcome {
	switch r.Operation {
	case "deploy_service":
		return e.deployService(ctx, r)
	case "rollback_deployment":
		return e.rollbackDeployment(ctx, r)
	case "restart_service":
		return e.restartService(ctx, r)
	case "get_deployment_status":
		return e.getDeploymentStatus(ctx, r)
	case "scale_service":
		return e.scaleService(ctx, r)
	default:
		return Outcome{Error: fmt.Sprintf("unsupported cloud operation %q", r.Operation)}
	}
}

func (e *CloudExecutor) deployService(ctx context.Context, r Request) Outcome {
	serviceName := text(r.Args, "service")
	image := text(r.Args, "image")
	if serviceName == "" {
		return Outcome{Error: "service is required"}
	}
	if image == "" {
		return Outcome{Error: "image is required"}
	}
	if err := e.checkServiceAllowed(serviceName); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	e.env.simMu.Lock()
	defer e.env.simMu.Unlock()

	svc, exists := e.env.services[serviceName]
	if !exists {
		svc = &SimulatedService{
			Name:     serviceName,
			Replicas: 1,
			Revision: 0,
		}
		e.env.services[serviceName] = svc
	}

	if svc.Image != "" {
		svc.PastImages = append(svc.PastImages, svc.Image)
	}
	svc.Image = image
	svc.Version = text(r.Args, "version")
	if svc.Version == "" {
		parts := strings.Split(image, ":")
		if len(parts) > 1 {
			svc.Version = parts[len(parts)-1]
		}
	}
	svc.Revision++
	svc.Status = "deployed"
	svc.LastDeployed = time.Now().UTC()

	resp := map[string]any{
		"service":       svc.Name,
		"environment":   e.env.ID,
		"image":         svc.Image,
		"version":       svc.Version,
		"revision":      svc.Revision,
		"status":        svc.Status,
		"replicas":      svc.Replicas,
		"last_deployed": svc.LastDeployed,
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}

func (e *CloudExecutor) rollbackDeployment(ctx context.Context, r Request) Outcome {
	serviceName := text(r.Args, "service")
	if serviceName == "" {
		return Outcome{Error: "service is required"}
	}
	if err := e.checkServiceAllowed(serviceName); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	e.env.simMu.Lock()
	defer e.env.simMu.Unlock()

	svc, exists := e.env.services[serviceName]
	if !exists {
		return Outcome{Status: 404, Error: fmt.Sprintf("service %q not found in environment %s", serviceName, e.env.ID)}
	}
	if len(svc.PastImages) == 0 {
		return Outcome{Status: 400, Error: fmt.Sprintf("service %q has no previous revisions to rollback to", serviceName)}
	}

	prevImage := svc.PastImages[len(svc.PastImages)-1]
	svc.PastImages = svc.PastImages[:len(svc.PastImages)-1]
	currentImage := svc.Image
	svc.Image = prevImage
	svc.Revision++
	svc.Status = "rolled_back"
	svc.LastDeployed = time.Now().UTC()

	resp := map[string]any{
		"service":        svc.Name,
		"environment":    e.env.ID,
		"rolled_back_to": prevImage,
		"previous_image": currentImage,
		"revision":       svc.Revision,
		"status":         svc.Status,
		"last_deployed":  svc.LastDeployed,
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}

func (e *CloudExecutor) restartService(ctx context.Context, r Request) Outcome {
	serviceName := text(r.Args, "service")
	if serviceName == "" {
		return Outcome{Error: "service is required"}
	}
	if err := e.checkServiceAllowed(serviceName); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	e.env.simMu.Lock()
	defer e.env.simMu.Unlock()

	svc, exists := e.env.services[serviceName]
	if !exists {
		return Outcome{Status: 404, Error: fmt.Sprintf("service %q not found in environment %s", serviceName, e.env.ID)}
	}

	svc.Status = "restarting"
	resp := map[string]any{
		"service":     svc.Name,
		"environment": e.env.ID,
		"status":      "restarted",
		"replicas":    svc.Replicas,
		"restarted_at": time.Now().UTC(),
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}

func (e *CloudExecutor) getDeploymentStatus(ctx context.Context, r Request) Outcome {
	serviceName := text(r.Args, "service")
	if serviceName == "" {
		return Outcome{Error: "service is required"}
	}
	if err := e.checkServiceAllowed(serviceName); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	e.env.simMu.RLock()
	defer e.env.simMu.RUnlock()

	svc, exists := e.env.services[serviceName]
	if !exists {
		return Outcome{Status: 404, Error: fmt.Sprintf("service %q not found in environment %s", serviceName, e.env.ID)}
	}

	resp := map[string]any{
		"service":       svc.Name,
		"environment":   e.env.ID,
		"image":         svc.Image,
		"version":       svc.Version,
		"revision":      svc.Revision,
		"status":        svc.Status,
		"replicas":      svc.Replicas,
		"last_deployed": svc.LastDeployed,
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}

func (e *CloudExecutor) scaleService(ctx context.Context, r Request) Outcome {
	serviceName := text(r.Args, "service")
	if serviceName == "" {
		return Outcome{Error: "service is required"}
	}
	if err := e.checkServiceAllowed(serviceName); err != nil {
		return Outcome{Status: 403, Error: err.Error()}
	}

	replicasRaw, ok := r.Args["replicas"]
	if !ok {
		return Outcome{Error: "replicas is required"}
	}
	replicas := 0
	switch v := replicasRaw.(type) {
	case float64:
		replicas = int(v)
	case int:
		replicas = v
	default:
		return Outcome{Error: "replicas must be an integer"}
	}

	if replicas < e.env.MinReplicas {
		return Outcome{Status: 400, Error: fmt.Sprintf("replicas %d is below minimum permitted bound %d for environment %s", replicas, e.env.MinReplicas, e.env.ID)}
	}
	if replicas > e.env.MaxReplicas {
		return Outcome{Status: 400, Error: fmt.Sprintf("replicas %d exceeds maximum permitted bound %d for environment %s", replicas, e.env.MaxReplicas, e.env.ID)}
	}

	e.env.simMu.Lock()
	defer e.env.simMu.Unlock()

	svc, exists := e.env.services[serviceName]
	if !exists {
		return Outcome{Status: 404, Error: fmt.Sprintf("service %q not found in environment %s", serviceName, e.env.ID)}
	}

	oldReplicas := svc.Replicas
	svc.Replicas = replicas

	resp := map[string]any{
		"service":      svc.Name,
		"environment":  e.env.ID,
		"old_replicas": oldReplicas,
		"new_replicas": svc.Replicas,
		"status":       "scaled",
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}
