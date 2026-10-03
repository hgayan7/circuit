package gateway

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenProvider returns a bearer credential for authenticating against GitHub API.
// For repository-scoped credentials (e.g. GitHub Apps), repository is provided as "owner/repo".
type TokenProvider interface {
	Token(ctx context.Context, repository string) (string, error)
}

// StaticTokenProvider wraps a fixed personal access token or OAuth credential.
type StaticTokenProvider struct {
	token string
}

// NewStaticTokenProvider creates a static provider.
func NewStaticTokenProvider(token string) *StaticTokenProvider {
	return &StaticTokenProvider{token: token}
}

// Token returns the static token.
func (p *StaticTokenProvider) Token(_ context.Context, _ string) (string, error) {
	if p.token == "" {
		return "", fmt.Errorf("GitHub token is not configured")
	}
	return p.token, nil
}

// ParseRSAPrivateKey parses an RSA private key from PEM bytes (PKCS#1 or PKCS#8).
func ParseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block containing RSA private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	pk8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		if rsaKey, ok := pk8.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, fmt.Errorf("PEM contains non-RSA private key")
	}
	return nil, fmt.Errorf("unable to parse private key as PKCS#1 or PKCS#8: %w", err)
}

// MintAppJWT generates a signed RS256 JWT for GitHub App authentication.
func MintAppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	if key == nil {
		return "", fmt.Errorf("private key is required")
	}
	if appID <= 0 {
		return "", fmt.Errorf("valid app ID is required")
	}
	headerJSON := `{"alg":"RS256","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	claims := struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{
		Iat: now.Add(-60 * time.Second).Unix(),
		Exp: now.Add(9 * time.Minute).Unix(),
		Iss: fmt.Sprintf("%d", appID),
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerB64 + "." + claimsB64
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigB64, nil
}

type cachedInstallationToken struct {
	token     string
	expiresAt time.Time
}

// GitHubAppTokenProvider mints short-lived, repository-scoped installation tokens
// using GitHub App credentials (App ID + Private Key).
type GitHubAppTokenProvider struct {
	appID          int64
	key            *rsa.PrivateKey
	installationID int64
	base           string
	client         *http.Client
	mu             sync.RWMutex
	cache          map[string]*cachedInstallationToken
	now            func() time.Time
}

// NewGitHubAppTokenProvider creates a GitHubAppTokenProvider.
func NewGitHubAppTokenProvider(appID int64, keyPEM []byte, installationID int64) (*GitHubAppTokenProvider, error) {
	key, err := ParseRSAPrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &GitHubAppTokenProvider{
		appID:          appID,
		key:            key,
		installationID: installationID,
		base:           "https://api.github.com",
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		cache: make(map[string]*cachedInstallationToken),
		now:   time.Now,
	}, nil
}

// Token retrieves an active, short-lived installation access token for the given repository.
// If cached and still valid (with 5-minute safety margin), the cached token is reused.
func (p *GitHubAppTokenProvider) Token(ctx context.Context, repository string) (string, error) {
	repository = strings.ToLower(repository)
	cacheKey := repository
	if cacheKey == "" && p.installationID > 0 {
		cacheKey = fmt.Sprintf("inst-%d", p.installationID)
	}

	p.mu.RLock()
	cached, ok := p.cache[cacheKey]
	now := p.now()
	if ok && cached != nil && now.Add(5*time.Minute).Before(cached.expiresAt) {
		p.mu.RUnlock()
		return cached.token, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Recheck cache under write lock
	cached, ok = p.cache[cacheKey]
	now = p.now()
	if ok && cached != nil && now.Add(5*time.Minute).Before(cached.expiresAt) {
		return cached.token, nil
	}

	jwt, err := MintAppJWT(p.appID, p.key, now)
	if err != nil {
		return "", fmt.Errorf("minting GitHub App JWT: %w", err)
	}

	instID := p.installationID
	if instID <= 0 {
		if repository == "" {
			return "", fmt.Errorf("installation_id is required when repository is not provided")
		}
		instID, err = p.findInstallationForRepo(ctx, jwt, repository)
		if err != nil {
			return "", fmt.Errorf("resolving installation for %s: %w", repository, err)
		}
	}

	token, expiresAt, err := p.requestInstallationToken(ctx, jwt, instID, repository)
	if err != nil {
		return "", fmt.Errorf("requesting installation access token: %w", err)
	}

	p.cache[cacheKey] = &cachedInstallationToken{
		token:     token,
		expiresAt: expiresAt,
	}
	return token, nil
}

func (p *GitHubAppTokenProvider) findInstallationForRepo(ctx context.Context, jwt, repository string) (int64, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/installation", p.base, repository)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding installation response: %w", err)
	}
	if result.ID <= 0 {
		return 0, fmt.Errorf("received invalid installation ID %d", result.ID)
	}
	return result.ID, nil
}

func (p *GitHubAppTokenProvider) requestInstallationToken(ctx context.Context, jwt string, instID int64, repository string) (string, time.Time, error) {
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", p.base, instID)
	var body io.Reader
	if repository != "" {
		parts := strings.Split(repository, "/")
		repoName := parts[len(parts)-1]
		payload := map[string]any{
			"repositories": []string{repoName},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return "", time.Time{}, err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", time.Time{}, fmt.Errorf("token request returned %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding token response: %w", err)
	}
	if result.Token == "" {
		return "", time.Time{}, fmt.Errorf("empty token in GitHub response")
	}
	expiresAt, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil {
		// Fallback to 1 hour from now if unparseable
		expiresAt = p.now().Add(1 * time.Hour)
	}
	return result.Token, expiresAt, nil
}
