package policy_test

import (
	"context"
	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/policy"
	"strings"
	"testing"
)

func TestHostAndEndpointMatchTogether(t *testing.T) {
	pol, err := config.ParsePolicy(strings.NewReader("name: scoped\nrules:\n- id: scope\n  match:\n    host: api.example.com\n    endpoint: DELETE /users\n  action: DENY\n"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(pol)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host, endpoint string
		action         config.ActionType
	}{{"api.example.com", "DELETE /users", config.ActionDeny}, {"other.example.com", "DELETE /users", config.ActionAllow}, {"api.example.com", "GET /users", config.ActionAllow}} {
		r, err := engine.Evaluate(context.Background(), &policy.EvaluationContext{Host: c.host, Endpoint: c.endpoint})
		if err != nil {
			t.Fatal(err)
		}
		if r.Action != c.action {
			t.Fatalf("host=%s endpoint=%s action=%s", c.host, c.endpoint, r.Action)
		}
	}
}
