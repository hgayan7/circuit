package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestGatewayOperatorClientBoundaries(t *testing.T) {
	for _, base := range []string{"https://user:secret@example.com", "https://example.com?token=secret", "http://example.com", "file:///state", "https://example.com/subpath"} {
		_, err := gatewayURL(base, "/readyz")
		require.Error(t, err, base)
	}
	value, err := gatewayURL("https://localhost:8443/", "/readyz")
	require.NoError(t, err)
	require.Equal(t, "https://localhost:8443/readyz", value)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.com", 302) }))
	defer server.Close()
	client, err := gatewayClient("")
	require.NoError(t, err)
	response, err := client.Get(server.URL)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	require.Equal(t, uint16(0x0304), client.Transport.(*http.Transport).TLSClientConfig.MinVersion)
	require.Error(t, serveGatewayTLS(&cobra.Command{}, "127.0.0.1:0", http.NotFoundHandler(), "missing-cert", ""))
}
