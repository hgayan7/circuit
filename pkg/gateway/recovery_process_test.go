package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type crashExecutor struct{ receipt, phase string }

func (e crashExecutor) Execute(ctx context.Context, _ Request) Outcome {
	if e.phase == "after" {
		if err := os.WriteFile(e.receipt, []byte("provider accepted exactly one write"), 0600); err != nil {
			return Outcome{Error: "fixture receipt failed"}
		}
	}
	<-ctx.Done()
	return Outcome{Uncertain: true, Error: "fixture response unavailable"}
}

// The child is a real HTTP gateway process, killed without deferred cleanup.
func TestRecoveryProcessHelper(t *testing.T) {
	if os.Getenv("CIRCUIT_RECOVERY_HELPER") != "1" {
		return
	}
	cfg := testConfig(t, `limits:
- id: one-write
  actions: [create_pr]
  scope: agent
  window: 1h
  max_calls: 1
`)
	store, err := OpenStore(os.Getenv("CIRCUIT_RECOVERY_STATE"))
	require.NoError(t, err)
	defer store.Close()
	svc, err := NewService(cfg, store, crashExecutor{os.Getenv("CIRCUIT_RECOVERY_RECEIPT"), os.Getenv("CIRCUIT_RECOVERY_PHASE")})
	require.NoError(t, err)
	h, err := NewHTTPHandler(svc, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	fmt.Println("http://" + listener.Addr().String())
	http.Serve(listener, h)
}

func startRecoveryChild(t *testing.T, dir, phase string) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryProcessHelper$")
	cmd.Env = append(os.Environ(), "CIRCUIT_RECOVERY_HELPER=1", "CIRCUIT_RECOVERY_STATE="+filepath.Join(dir, "state.db"), "CIRCUIT_RECOVERY_RECEIPT="+filepath.Join(dir, "receipt"), "CIRCUIT_RECOVERY_PHASE="+phase)
	pipe, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case address := <-ready:
		require.True(t, strings.HasPrefix(address, "http://"), address)
		return cmd, address
	case <-time.After(10 * time.Second):
		t.Fatal("child readiness timeout")
		return nil, ""
	}
}

func TestProcessKillDoesNotReplayClaimedWrites(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			child, address := startRecoveryChild(t, dir, phase)
			body, _ := json.Marshal(prRequest())
			request, err := http.NewRequest("POST", address+"/v1/actions", strings.NewReader(string(body)))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer "+agentToken)
			request.Header.Set("Idempotency-Key", "crash-once")
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
				if err == nil {
					response.Body.Close()
				}
			}()
			t.Cleanup(func() { <-finished })
			var action Action
			require.Eventually(t, func() bool {
				status, data := callHTTP(t, address+"/v1/actions", "GET", agentToken, "", nil)
				if status != 200 {
					return false
				}
				var actions []Action
				if json.Unmarshal(data, &actions) != nil || len(actions) == 0 {
					return false
				}
				action = actions[0]
				if phase == "after" {
					if _, err := os.Stat(filepath.Join(dir, "receipt")); err != nil {
						return false
					}
				}
				return action.State == "executing"
			}, 5*time.Second, 20*time.Millisecond)
			require.NoError(t, child.Process.Kill())
			require.Error(t, child.Wait())
			<-finished
			if phase == "before" {
				_, err = os.Stat(filepath.Join(dir, "receipt"))
				require.True(t, os.IsNotExist(err))
			}
			restarted, newAddress := startRecoveryChild(t, dir, "after")
			status, data := callHTTP(t, newAddress+"/v1/actions", "POST", agentToken, "crash-once", prRequest())
			require.Equal(t, 202, status)
			var retried Action
			require.NoError(t, json.Unmarshal(data, &retried))
			require.Equal(t, action.ID, retried.ID)
			require.Equal(t, "uncertain", retried.State)
			request2 := prRequest()
			request2.Args["title"] = "another write"
			status, data = callHTTP(t, newAddress+"/v1/actions", "POST", agentToken, "new-write", request2)
			require.Equal(t, 403, status)
			require.Contains(t, string(data), "budget")
			if phase == "before" {
				_, err = os.Stat(filepath.Join(dir, "receipt"))
				require.True(t, os.IsNotExist(err))
			}
			restarted.Process.Kill()
			restarted.Wait()
		})
	}
}
