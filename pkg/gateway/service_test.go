package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

type countingExecutor struct {
	calls atomic.Int32
	fn    func(Request) Outcome
}

func (e *countingExecutor) Execute(_ context.Context, r Request) Outcome {
	e.calls.Add(1)
	if e.fn != nil {
		return e.fn(r)
	}
	return Outcome{Status: 201, Body: json.RawMessage(`{"ok":true}`)}
}
func testConfig(t *testing.T, extra string) *Config {
	t.Helper()
	c, err := ParseConfig(strings.NewReader(`name: test
agents:
- id: agent
  token_env: AGENT_TOKEN
  repositories: [acme/app]
  actions: [create_pr, merge_pr, put_file, create_branch, create_issue, read_file, get_pr, update_issue]
` + extra))
	require.NoError(t, err)
	return c
}
func testService(t *testing.T, c *Config, e Executor) (*Service, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(file)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	s, err := NewService(c, store, e)
	require.NoError(t, err)
	return s, file
}
func prRequest() Request {
	return Request{Operation: "create_pr", Repository: "acme/app", Args: map[string]any{"title": "Fix bug", "head": "circuit/fix", "base": "main"}}
}
func mergeRequest() Request {
	return Request{Operation: "merge_pr", Repository: "acme/app", Args: map[string]any{"number": float64(1), "sha": DemoSHA}}
}
func TestScopesAndMandatoryApproval(t *testing.T) {
	e := &countingExecutor{}
	s, _ := testService(t, testConfig(t, ""), e)
	req := prRequest()
	req.Repository = "other/app"
	a, err := s.Submit(context.Background(), "agent", "outside", req)
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	req = prRequest()
	req.Args["head"] = "main"
	a, err = s.Submit(context.Background(), "agent", "main", req)
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	req = Request{Operation: "put_file", Repository: "acme/app", Args: map[string]any{"path": ".github/workflows/ci.yml", "branch": "circuit/fix", "content": "aGVsbG8=", "message": "Change"}}
	a, err = s.Submit(context.Background(), "agent", "workflow", req)
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	a, err = s.Submit(context.Background(), "agent", "merge", mergeRequest())
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	require.Zero(t, e.calls.Load())
	_, err = s.Decide(context.Background(), a.ID, "wrong-digest", "approve")
	require.Error(t, err)
	require.Zero(t, e.calls.Load())
	approved, err := s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", approved.State)
	require.EqualValues(t, 1, e.calls.Load())
	_, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.Error(t, err)
	require.EqualValues(t, 1, e.calls.Load())
	events, err := s.store.Events(a.ID)
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, "operator", events[1].Actor)
}
func TestAllConstraintsApplyDespiteAllowRule(t *testing.T) {
	c := testConfig(t, `rules:
- id: allow
  actions: [create_pr, merge_pr]
  action: ALLOW
- id: review
  actions: [create_pr]
  action: REQUIRE_APPROVAL
- id: forbidden-title
  actions: [create_pr]
  condition: args.title == "Forbidden"
  action: DENY
  reason: Forbidden title
limits:
- id: pr-cap
  actions: [create_pr]
  scope: agent
  window: 1h
  max_calls: 1
`)
	e := &countingExecutor{}
	s, _ := testService(t, c, e)
	req := prRequest()
	req.Args["title"] = "Forbidden"
	a, err := s.Submit(context.Background(), "agent", "deny", req)
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	a, err = s.Submit(context.Background(), "agent", "review", prRequest())
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	other := prRequest()
	other.Args["title"] = "Another PR"
	b, err := s.Submit(context.Background(), "agent", "over", other)
	require.NoError(t, err)
	require.Equal(t, "denied", b.State)
	require.Zero(t, e.calls.Load())
	_, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.EqualValues(t, 1, e.calls.Load())
	a, err = s.Submit(context.Background(), "agent", "merge", mergeRequest())
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State)
	require.Equal(t, "policy", a.ApprovedBy)
}
func TestConcurrentIdempotencyDispatchesOnce(t *testing.T) {
	e := &countingExecutor{}
	s, _ := testService(t, testConfig(t, ""), e)
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for j := 0; j < 30; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Submit(context.Background(), "agent", "same", prRequest())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, e.calls.Load())
	req := prRequest()
	req.Args["title"] = "Different"
	_, err := s.Submit(context.Background(), "agent", "same", req)
	require.Error(t, err)
	require.EqualValues(t, 1, e.calls.Load())
}
func TestConcurrentBudgetsAndRestartPersistence(t *testing.T) {
	c := testConfig(t, `limits:
- id: repo-cap
  actions: [create_pr]
  scope: repository
  window: 1h
  max_calls: 2
- id: agent-cap
  actions: [create_pr]
  scope: agent
  window: 1h
  max_calls: 1
`)
	e := &countingExecutor{}
	s, file := testService(t, c, e)
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, err := s.Submit(context.Background(), "agent", k, prRequest())
			if err != nil {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	require.EqualValues(t, 1, e.calls.Load())
	require.NoError(t, s.store.Close())
	store, err := OpenStore(file)
	require.NoError(t, err)
	defer store.Close()
	next, err := NewService(c, store, e)
	require.NoError(t, err)
	a, err := next.Submit(context.Background(), "agent", "after-restart", prRequest())
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	require.EqualValues(t, 1, e.calls.Load())
}
func TestPendingReservationReleasedOnRejection(t *testing.T) {
	c := testConfig(t, `limits:
- id: merge-cap
  actions: [merge_pr]
  scope: agent
  window: 1h
  max_calls: 1
`)
	e := &countingExecutor{}
	s, _ := testService(t, c, e)
	first, err := s.Submit(context.Background(), "agent", "one", mergeRequest())
	require.NoError(t, err)
	other := mergeRequest()
	other.Args["number"] = float64(2)
	a, err := s.Submit(context.Background(), "agent", "two", other)
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	_, err = s.Decide(context.Background(), first.ID, first.Digest, "reject")
	require.NoError(t, err)
	a, err = s.Submit(context.Background(), "agent", "three", mergeRequest())
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
}
func TestUnknownOutcomeNeverAutomaticallyReplays(t *testing.T) {
	e := &countingExecutor{fn: func(Request) Outcome { return Outcome{Error: "timeout", Uncertain: true} }}
	s, _ := testService(t, testConfig(t, ""), e)
	a, err := s.Submit(context.Background(), "agent", "uncertain", prRequest())
	require.NoError(t, err)
	require.Equal(t, "uncertain", a.State)
	again, err := s.Submit(context.Background(), "agent", "uncertain", prRequest())
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.EqualValues(t, 1, e.calls.Load())
	differentKey, err := s.Submit(context.Background(), "agent", "new-key-same-uncertain-action", prRequest())
	require.NoError(t, err)
	require.Equal(t, a.ID, differentKey.ID)
	require.EqualValues(t, 1, e.calls.Load())
	_, err = s.Reconcile(a.ID, a.Digest, "succeeded", "Verified PR #1 exists")
	require.NoError(t, err)
	_, err = s.Submit(context.Background(), "agent", "uncertain", prRequest())
	require.NoError(t, err)
	require.EqualValues(t, 1, e.calls.Load())
}
func TestRestartRecoversClaimedExecutionAsUncertain(t *testing.T) {
	c := testConfig(t, "")
	e := &countingExecutor{}
	s, file := testService(t, c, e)
	a, err := s.Submit(context.Background(), "agent", "pending", mergeRequest())
	require.NoError(t, err)
	require.NoError(t, s.store.db.Update(func(tx *bolt.Tx) error { a.State = "executing"; return saveAction(tx, a, "system") }))
	require.NoError(t, s.store.Close())
	store, err := OpenStore(file)
	require.NoError(t, err)
	defer store.Close()
	next, err := NewService(c, store, e)
	require.NoError(t, err)
	recovered, err := next.Submit(context.Background(), "agent", "pending", mergeRequest())
	require.NoError(t, err)
	require.Equal(t, "uncertain", recovered.State)
	require.Zero(t, e.calls.Load())
}
func TestApprovalExpiryPolicyChangesAndStoredPayloadIntegrity(t *testing.T) {
	c := testConfig(t, "")
	e := &countingExecutor{}
	s, _ := testService(t, c, e)
	a, err := s.Submit(context.Background(), "agent", "expiry", mergeRequest())
	require.NoError(t, err)
	require.NoError(t, s.store.db.Update(func(tx *bolt.Tx) error { a.ExpiresAt = time.Now().Add(-time.Minute); return saveAction(tx, a, "test") }))
	expired, err := s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "expired", expired.State)
	require.Zero(t, e.calls.Load())
	a, err = s.Submit(context.Background(), "agent", "change", mergeRequest())
	require.NoError(t, err)
	s.cfg = testConfig(t, "rules:\n- id: deny-merges\n  actions: [merge_pr]\n  action: DENY\n")
	denied, err := s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "denied", denied.State)
	require.Zero(t, e.calls.Load())
	s.cfg = c
	a, err = s.Submit(context.Background(), "agent", "tamper", mergeRequest())
	require.NoError(t, err)
	require.NoError(t, s.store.db.Update(func(tx *bolt.Tx) error {
		a.Request.Args["sha"] = strings.Repeat("b", 40)
		return saveAction(tx, a, "test")
	}))
	_, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.Error(t, err)
	require.Zero(t, e.calls.Load())
}
