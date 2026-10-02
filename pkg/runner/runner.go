package runner

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/himshikhargayan/circuit/pkg/approval"
	"github.com/himshikhargayan/circuit/pkg/audit"
	"github.com/himshikhargayan/circuit/pkg/interceptor/proxy"
	"github.com/himshikhargayan/circuit/pkg/policy"
)

// Option configures runner execution settings.
type Option func(*Runner)

// WithTargetURL sets the target base URL for reverse proxying.
func WithTargetURL(u *url.URL) Option {
	return func(r *Runner) {
		r.targetURL = u
	}
}

// WithAuditRecorder sets the audit recorder.
func WithAuditRecorder(rec audit.Recorder) Option {
	return func(r *Runner) {
		r.recorder = rec
	}
}

// WithApprovalProvider sets the approval provider.
func WithApprovalProvider(a approval.Provider) Option {
	return func(r *Runner) {
		r.approver = a
	}
}

// WithInjectedToken sets the downstream token to inject.
func WithInjectedToken(token string) Option {
	return func(r *Runner) {
		r.injectedToken = token
	}
}

// Runner starts a local ephemeral proxy and executes a child command with proxy env vars.
type Runner struct {
	engine        *policy.Engine
	targetURL     *url.URL
	recorder      audit.Recorder
	approver      approval.Provider
	injectedToken string
}

// New creates an initialized Runner instance.
func New(engine *policy.Engine, opts ...Option) *Runner {
	r := &Runner{
		engine: engine,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RunCommand starts an ephemeral proxy on loopback and launches the child command.
func (r *Runner) RunCommand(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, command string, args ...string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to bind ephemeral loopback listener: %w", err)
	}
	defer listener.Close()

	proxyAddr := fmt.Sprintf("http://%s", listener.Addr().String())

	var proxyOpts []proxy.HandlerOption
	if r.recorder != nil {
		proxyOpts = append(proxyOpts, proxy.WithAuditRecorder(r.recorder))
	}
	if r.approver != nil {
		proxyOpts = append(proxyOpts, proxy.WithApprovalProvider(r.approver))
	}
	if r.injectedToken != "" {
		proxyOpts = append(proxyOpts, proxy.WithInjectedToken(r.injectedToken))
	}

	target := r.targetURL
	if target == nil {
		target, _ = url.Parse("http://127.0.0.1")
	}

	handler := proxy.NewHandler(r.engine, target, proxyOpts...)
	server := &http.Server{Handler: handler}

	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Inject proxy environment variables into child process
	cmd.Env = append(os.Environ(),
		"HTTP_PROXY="+proxyAddr,
		"HTTPS_PROXY="+proxyAddr,
		"http_proxy="+proxyAddr,
		"https_proxy="+proxyAddr,
		"ALL_PROXY="+proxyAddr,
	)

	cmdErr := cmd.Run()

	// Graceful shutdown of ephemeral server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)

	return cmdErr
}
