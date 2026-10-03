package gateway

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitHubAppLiveScopeAndRefresh(t *testing.T) {
	path := os.Getenv("CIRCUIT_PILOT_APP_KEY")
	if path == "" {
		t.Skip("set CIRCUIT_PILOT_APP_ID, CIRCUIT_PILOT_APP_KEY, and CIRCUIT_PILOT_REPO for opt-in live App test")
	}
	id, err := strconv.ParseInt(os.Getenv("CIRCUIT_PILOT_APP_ID"), 10, 64)
	require.NoError(t, err)
	key, err := os.ReadFile(path)
	require.NoError(t, err)
	repo := os.Getenv("CIRCUIT_PILOT_REPO")
	require.Contains(t, repo, "/circuit-gateway-")
	require.Contains(t, repo, "pilot-")
	p, err := NewGitHubAppTokenProvider(id, key, 0)
	require.NoError(t, err)
	token, err := p.Token(context.Background(), repo)
	require.NoError(t, err)
	validate := func(token string) {
		req, err := http.NewRequest("GET", "https://api.github.com/installation/repositories", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		var body struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Repositories, 1)
		require.Equal(t, strings.ToLower(repo), strings.ToLower(body.Repositories[0].FullName))
	}
	validate(token)
	p.mu.Lock()
	p.cache[repo].expiresAt = time.Now()
	p.mu.Unlock()
	refreshed, err := p.Token(context.Background(), repo)
	require.NoError(t, err)
	validate(refreshed)
	require.True(t, p.cache[repo].expiresAt.After(time.Now().Add(5*time.Minute)))
}
