// Package forward implements explicit HTTP proxying and HTTPS inspection via CONNECT.
package forward

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/elazarl/goproxy"
	"github.com/hgayan7/circuit/pkg/interceptor/proxy"
	"github.com/hgayan7/circuit/pkg/policy"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

type Handler struct{ proxy *goproxy.ProxyHttpServer }

func NewHandler(engine *policy.Engine, ca *CA, opts ...proxy.HandlerOption) (*Handler, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if certPath := os.Getenv("SSL_CERT_FILE"); certPath != "" {
		data, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("read upstream trust file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("upstream trust file contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}

	p := goproxy.NewProxyHttpServer()
	// goproxy's defaults skip upstream verification; always replace them.
	p.Tr = &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true}
	p.ConnectDial = nil
	p.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		return &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&ca.Certificate)}, host
	})
	gate := proxy.NewHandler(engine, nil, opts...)
	p.OnRequest().DoFunc(func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			return r, errorResponse(r, 400, "Unsupported URL scheme")
		}
		if r.URL.Host == "" || r.URL.User != nil {
			return r, errorResponse(r, 400, "Invalid upstream URL")
		}
		if r.Header.Get("Upgrade") != "" {
			return r, errorResponse(r, 400, "Protocol upgrades cannot be inspected")
		}
		w := httptest.NewRecorder()
		if !gate.Authorize(w, r) {
			ctx.UserData = "circuit-denied"
			return r, w.Result()
		}
		// Never forward credentials intended for the local proxy.
		r.Header.Del("Proxy-Authorization")
		r.Header.Del("Proxy-Connection")
		if engine.InspectResponses() {
			r.Header.Set("Accept-Encoding", "identity")
		}
		return r, nil
	})
	p.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil {
			return errorResponse(ctx.Req, 502, "Upstream connection failed")
		}
		// Locally generated denials are already safe and must retain their status.
		if ctx.UserData == "circuit-denied" {
			return resp
		}
		if err := gate.InspectResponse(resp); err != nil {
			return errorResponse(ctx.Req, 502, err.Error())
		}
		return resp
	})
	return &Handler{proxy: p}, nil
}
func errorResponse(r *http.Request, status int, reason string) *http.Response {
	resp := goproxy.NewResponse(r, "text/plain", status, fmt.Sprintln(reason))
	return resp
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect && !r.URL.IsAbs() {
		http.Error(w, "Use Circuit as an HTTP proxy", 400)
		return
	}
	if r.Method == http.MethodConnect && (strings.ContainsAny(r.Host, "/\\ \t\r\n") || r.Host == "") {
		http.Error(w, "Invalid CONNECT authority", 400)
		return
	}
	h.proxy.ServeHTTP(w, r)
}
