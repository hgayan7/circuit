package circuitclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Circuit is the safety-aware facade over the generated agent API.
type Circuit struct {
	client *APIClient
	token  string
}
type ActionStopped struct{ Action *Action }

func (e *ActionStopped) Error() string {
	return fmt.Sprintf("Circuit action %s: %s: %s", e.Action.Id, e.Action.State, e.Action.Reason)
}

func NewCircuit(origin, token string, caPEM []byte) (*Circuit, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("Circuit URL must be an HTTPS origin")
	}
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("a scoped single-line agent token is required")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if len(caPEM) > 0 && !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("invalid Circuit CA")
	}
	cfg := NewConfiguration()
	cfg.Servers = ServerConfigurations{{URL: strings.TrimRight(origin, "/")}}
	cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Circuit{client: NewAPIClient(cfg), token: token}, nil
}

func FromEnvironment() (*Circuit, error) {
	token, err := os.ReadFile(os.Getenv("CIRCUIT_TOKEN_FILE"))
	if err != nil {
		return nil, fmt.Errorf("agent token file unavailable")
	}
	var ca []byte
	if path := os.Getenv("CIRCUIT_CA_CERT"); path != "" {
		ca, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("Circuit CA file unavailable")
		}
	}
	return NewCircuit(os.Getenv("CIRCUIT_GATEWAY_URL"), strings.TrimSpace(string(token)), ca)
}

func (c *Circuit) Submit(ctx context.Context, request ActionRequest, key string) (*Action, error) {
	ctx = context.WithValue(ctx, ContextAccessToken, c.token)
	action, response, err := c.client.ActionsAPI.SubmitAction(ctx).IdempotencyKey(key).ActionRequest(request).Execute()
	if err != nil && response != nil && (response.StatusCode == 403 || response.StatusCode == 410) {
		if apiErr, ok := err.(*GenericOpenAPIError); ok {
			var decoded Action
			if json.Unmarshal(apiErr.Body(), &decoded) == nil && decoded.Id != "" && decoded.State != "" {
				return &decoded, nil
			}
		}
	}
	return action, err
}
func (c *Circuit) Get(ctx context.Context, id string) (*Action, error) {
	action, _, err := c.client.ActionsAPI.GetAction(context.WithValue(ctx, ContextAccessToken, c.token), id).Execute()
	return action, err
}

// Execute only polls after submission. It never retries a provider dispatch or an uncertain action.
func (c *Circuit) Execute(ctx context.Context, request ActionRequest, key string) (*Action, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	action, err := c.Submit(ctx, request, key)
	if err != nil {
		return nil, err
	}
	for action.State == "pending" || action.State == "approved" || action.State == "executing" {
		select {
		case <-ctx.Done():
			return action, &ActionStopped{Action: action}
		case <-time.After(time.Second):
		}
		action, err = c.Get(ctx, action.Id)
		if err != nil {
			return nil, err
		}
	}
	if action.State != "succeeded" {
		return action, &ActionStopped{Action: action}
	}
	return action, nil
}
