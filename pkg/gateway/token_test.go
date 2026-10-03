package gateway

import (
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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func generateTestRSAKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: der,
	})
	return key, pemData
}

func generateTestPKCS8Key(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
	return key, pemData
}

func TestStaticTokenProvider(t *testing.T) {
	p := NewStaticTokenProvider("test-token")
	tok, err := p.Token(context.Background(), "acme/repo")
	require.NoError(t, err)
	require.Equal(t, "test-token", tok)

	empty := NewStaticTokenProvider("")
	_, err = empty.Token(context.Background(), "acme/repo")
	require.Error(t, err)
}

func TestParseRSAPrivateKey(t *testing.T) {
	_, pkcs1PEM := generateTestRSAKey(t)
	key1, err := ParseRSAPrivateKey(pkcs1PEM)
	require.NoError(t, err)
	require.NotNil(t, key1)

	_, pkcs8PEM := generateTestPKCS8Key(t)
	key8, err := ParseRSAPrivateKey(pkcs8PEM)
	require.NoError(t, err)
	require.NotNil(t, key8)

	_, err = ParseRSAPrivateKey([]byte("not a pem"))
	require.Error(t, err)

	invalidPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bogus")})
	_, err = ParseRSAPrivateKey(invalidPEM)
	require.Error(t, err)
}

func TestMintAppJWTAndVerify(t *testing.T) {
	key, _ := generateTestRSAKey(t)
	now := time.Now()
	jwt, err := MintAppJWT(123456, key, now)
	require.NoError(t, err)

	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3)

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]string
	require.NoError(t, json.Unmarshal(headerBytes, &header))
	require.Equal(t, "RS256", header["alg"])
	require.Equal(t, "JWT", header["typ"])

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	require.NoError(t, json.Unmarshal(claimsBytes, &claims))
	require.Equal(t, "123456", claims.Iss)
	require.Equal(t, now.Add(-60*time.Second).Unix(), claims.Iat)
	require.Equal(t, now.Add(9*time.Minute).Unix(), claims.Exp)

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)

	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))
	err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], sigBytes)
	require.NoError(t, err, "JWT signature must verify with the public key")
}

func TestGitHubAppTokenProviderWithMockServer(t *testing.T) {
	key, pemData := generateTestRSAKey(t)
	var tokenCalls atomic.Int32
	var installLookupCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		require.True(t, strings.HasPrefix(auth, "Bearer "))
		jwt := strings.TrimPrefix(auth, "Bearer ")

		// Verify JWT signature using key
		parts := strings.Split(jwt, ".")
		require.Len(t, parts, 3)
		sigBytes, _ := base64.RawURLEncoding.DecodeString(parts[2])
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		require.NoError(t, rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], sigBytes))

		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/acme/app/installation":
			installLookupCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":98765}`)
		case r.Method == "POST" && r.URL.Path == "/app/installations/98765/access_tokens":
			tokenCalls.Add(1)
			var body struct {
				Repositories []string `json:"repositories"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			require.Equal(t, []string{"app"}, body.Repositories)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"ghs_mock_installation_token_123","expires_at":"2099-01-01T00:00:00Z"}`)
		default:
			t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()

	// 1. Explicit installation ID
	p, err := NewGitHubAppTokenProvider(123456, pemData, 98765)
	require.NoError(t, err)
	p.base = server.URL

	tok1, err := p.Token(context.Background(), "acme/app")
	require.NoError(t, err)
	require.Equal(t, "ghs_mock_installation_token_123", tok1)
	require.EqualValues(t, 1, tokenCalls.Load())
	require.EqualValues(t, 0, installLookupCalls.Load())

	// 2. Cache hit test (should not call server again)
	tok2, err := p.Token(context.Background(), "acme/app")
	require.NoError(t, err)
	require.Equal(t, tok1, tok2)
	require.EqualValues(t, 1, tokenCalls.Load())

	// 3. Auto-discovery of installation ID (installationID = 0)
	pAuto, err := NewGitHubAppTokenProvider(123456, pemData, 0)
	require.NoError(t, err)
	pAuto.base = server.URL

	tokAuto, err := pAuto.Token(context.Background(), "acme/app")
	require.NoError(t, err)
	require.Equal(t, "ghs_mock_installation_token_123", tokAuto)
	require.EqualValues(t, 1, installLookupCalls.Load())
	require.EqualValues(t, 2, tokenCalls.Load())
}

func TestGitHubAppTokenExpiryAndRefresh(t *testing.T) {
	_, pemData := generateTestRSAKey(t)
	var tokenCalls atomic.Int32
	simulatedTime := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum := tokenCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// Return token expiring in 10 minutes
		expires := simulatedTime.Add(10 * time.Minute).Format(time.RFC3339)
		fmt.Fprintf(w, `{"token":"ghs_token_call_%d","expires_at":%q}`, callNum, expires)
	}))
	defer server.Close()

	p, err := NewGitHubAppTokenProvider(123456, pemData, 999)
	require.NoError(t, err)
	p.base = server.URL
	p.now = func() time.Time { return simulatedTime }

	// First call -> mints token 1
	tok1, err := p.Token(context.Background(), "acme/repo")
	require.NoError(t, err)
	require.Equal(t, "ghs_token_call_1", tok1)
	require.EqualValues(t, 1, tokenCalls.Load())

	// Call after 3 minutes (remaining: 7 min > 5 min margin) -> cached token 1
	simulatedTime = simulatedTime.Add(3 * time.Minute)
	tok2, err := p.Token(context.Background(), "acme/repo")
	require.NoError(t, err)
	require.Equal(t, "ghs_token_call_1", tok2)
	require.EqualValues(t, 1, tokenCalls.Load())

	// Call after 6 minutes (remaining: 4 min <= 5 min margin) -> refreshes token 2!
	simulatedTime = simulatedTime.Add(3 * time.Minute)
	tok3, err := p.Token(context.Background(), "acme/repo")
	require.NoError(t, err)
	require.Equal(t, "ghs_token_call_2", tok3)
	require.EqualValues(t, 2, tokenCalls.Load())
}
