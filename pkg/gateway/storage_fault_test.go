package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestReadOnlyStorageStopsDispatchAndReadiness(t *testing.T) {
	cfg := testConfig(t, "")
	service, path := testService(t, cfg, &countingExecutor{})
	require.NoError(t, service.store.Close())
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true})
	require.NoError(t, err)
	defer db.Close()
	store := &Store{db: db}
	executor := &countingExecutor{}
	// A running store becoming unwritable must fail closed, without a provider call.
	service.store = store
	service.executor = executor
	h, err := NewHTTPHandler(service, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	server := httptest.NewServer(h)
	defer server.Close()
	status, _ := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "unwritable", prRequest())
	require.Equal(t, 503, status)
	require.Zero(t, executor.calls.Load())
	status, _ = callHTTP(t, server.URL+"/readyz", "GET", "", "", nil)
	require.Equal(t, 503, status)
}

// Run this opt-in test in a network-disabled Docker container with a 2 MiB tmpfs.
// Ordinary CI never fills the host filesystem.
func TestDiskFullStorageHelper(t *testing.T) {
	if os.Getenv("CIRCUIT_DISK_FULL_TEST") != "1" {
		t.Skip("requires disposable bounded Docker tmpfs")
	}
	path := filepath.Join("/state", "gateway.db")
	store, err := OpenStore(path)
	require.NoError(t, err)
	executor := &countingExecutor{}
	svc, err := NewService(testConfig(t, ""), store, executor)
	require.NoError(t, err)
	var failedKey string
	var failedRequest Request
	for i := 0; i < 100; i++ {
		request := prRequest()
		request.Args["title"] = fmt.Sprintf("disk fixture %d", i)
		request.Args["body"] = strings.Repeat("fixture-data-", 30000)
		key := fmt.Sprintf("disk-%d", i)
		_, err = svc.Submit(context.Background(), "agent", key, request)
		if err != nil {
			failedKey = key
			failedRequest = request
			break
		}
	}
	require.NotEmpty(t, failedKey, "tmpfs did not fill")
	require.True(t, errors.Is(err, ErrStorageUnavailable), err)
	require.Error(t, store.Check())
	previous := executor.calls.Load()
	_, err = svc.Submit(context.Background(), "agent", "blocked-after-full", prRequest())
	require.ErrorIs(t, err, ErrStorageUnavailable)
	require.Equal(t, previous, executor.calls.Load())
	require.NoError(t, store.Close())
	// Old committed state must remain readable. An interrupted outcome may be uncertain.
	reopened, err := OpenStore(path)
	require.NoError(t, err)
	defer reopened.Close()
	recoveredExecutor := &countingExecutor{}
	recovered, err := NewService(testConfig(t, ""), reopened, recoveredExecutor)
	if err != nil {
		require.ErrorIs(t, err, ErrStorageUnavailable)
		return
	}
	// Read an existing key directly: do not propose a new provider write after full disk.
	err = reopened.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(keysBucket).Get([]byte("agent:" + failedKey))
		if id == nil {
			return nil
		}
		a, err := getAction(tx, string(id))
		if err != nil {
			return err
		}
		require.Equal(t, hashRequest("agent", failedRequest), a.Digest)
		require.NotEqual(t, "executing", a.State)
		return nil
	})
	require.NoError(t, err)
	_ = recovered
	require.Zero(t, recoveredExecutor.calls.Load())
}
