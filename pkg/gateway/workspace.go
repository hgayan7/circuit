package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// Workspace pins a file-operation root; shell isolation requires an external sandbox.
type Workspace struct {
	ID           string
	Root         string
	ReadOnly     bool
	AllowedCmds  []string
	DeniedCmds   []string
	MaxTimeout   time.Duration
	EnvAllowlist []string
	root         *os.Root
}

// NewWorkspace validates and constructs a Workspace with an absolute, clean root path.
func NewWorkspace(id, rootPath string, readOnly bool, maxTimeout time.Duration) (*Workspace, error) {
	if id == "" {
		return nil, fmt.Errorf("workspace ID is required")
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(rootPath))
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	info, err := os.Stat(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("workspace root does not exist: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root must be a directory")
	}
	// Evaluate symlinks in root itself to prevent root canonicalization confusion
	realRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root symlinks: %w", err)
	}
	if maxTimeout <= 0 {
		maxTimeout = 60 * time.Second
	}
	root, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, err
	}
	return &Workspace{
		ID:         id,
		Root:       realRoot,
		ReadOnly:   readOnly,
		MaxTimeout: maxTimeout,
		root:       root,
	}, nil
}

func (w *Workspace) Close() error { return w.root.Close() }

// ResolvePath checks paths for cwd and display. File I/O additionally uses os.Root
// to enforce containment during the operation, including concurrent symlink changes.
func (w *Workspace) ResolvePath(relPath string) (string, error) {
	relPath = filepath.Clean(relPath)
	if relPath == "." || relPath == "" {
		return w.Root, nil
	}
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "\\") {
		return "", fmt.Errorf("path must be relative to workspace root")
	}
	if strings.HasPrefix(relPath, "..") || strings.Contains(relPath, "/../") || strings.Contains(relPath, "\\..\\") {
		return "", fmt.Errorf("path traversal outside workspace root is forbidden")
	}
	target := filepath.Join(w.Root, relPath)
	// Check relative distance from root
	rel, err := filepath.Rel(w.Root, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path resolves outside workspace root")
	}
	// If the file/path already exists, verify its resolved symlink stays inside root
	if realTarget, err := filepath.EvalSymlinks(target); err == nil {
		relReal, err := filepath.Rel(w.Root, realTarget)
		if err != nil || strings.HasPrefix(relReal, "..") {
			return "", fmt.Errorf("symlink target escapes workspace root")
		}
	}
	return target, nil
}

// IsDestructiveCommand analyzes a shell command string to detect potentially dangerous or mutating commands.
func IsDestructiveCommand(cmdStr string) (bool, string) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmdStr), "")
	if err != nil {
		return true, "unparseable command syntax"
	}
	destructiveVerbs := map[string]bool{
		"rm": true, "rmdir": true, "mkfs": true, "dd": true,
		"shred": true, "truncate": true, "chmod": true, "chown": true,
		"sudo": true, "su": true, "kill": true, "killall": true, "pkill": true,
		"reboot": true, "shutdown": true, "init": true, "systemctl": true,
	}

	var isDestructive bool
	var reason string

	syntax.Walk(f, func(n syntax.Node) bool {
		if isDestructive {
			return false
		}
		switch node := n.(type) {
		case *syntax.CallExpr:
			if len(node.Args) > 0 {
				var words []string
				for _, a := range node.Args {
					if lit, ok := literalWord(a); ok {
						words = append(words, lit)
					}
				}
				if len(words) > 0 {
					baseCmd := filepath.Base(words[0])
					if destructiveVerbs[baseCmd] {
						isDestructive = true
						reason = fmt.Sprintf("destructive utility %q requires operator review", baseCmd)
						return false
					}
					// Git destructive operations
					if baseCmd == "git" && len(words) > 1 {
						subCmd := words[1]
						if subCmd == "reset" || subCmd == "clean" || subCmd == "push" || subCmd == "rebase" {
							isDestructive = true
							reason = fmt.Sprintf("consequential git operation %q requires operator review", subCmd)
							return false
						}
					}
				}
			}
		}
		return true
	})

	return isDestructive, reason
}

// ShellExecutor executes bounded commands and file operations within a configured Workspace.
type ShellExecutor struct {
	workspace *Workspace
}

// NewShellExecutor creates an executor for a given workspace.
func NewShellExecutor(ws *Workspace) *ShellExecutor {
	return &ShellExecutor{workspace: ws}
}

func (e *ShellExecutor) Execute(ctx context.Context, r Request) Outcome {
	switch r.Operation {
	case "exec_cmd":
		return e.execCmd(ctx, r)
	case "read_file":
		return e.readFile(ctx, r)
	case "write_file":
		return e.writeFile(ctx, r)
	case "delete_file":
		return e.deleteFile(ctx, r)
	case "list_dir":
		return e.listDir(ctx, r)
	default:
		return Outcome{Error: fmt.Sprintf("unsupported shell/file operation %q", r.Operation)}
	}
}

func (e *ShellExecutor) execCmd(ctx context.Context, r Request) Outcome {
	if e.workspace.ReadOnly {
		return Outcome{Status: 403, Error: "shell execution is forbidden in a read-only workspace"}
	}
	cmdStr := text(r.Args, "command")
	if strings.TrimSpace(cmdStr) == "" {
		return Outcome{Error: "command is required"}
	}

	timeout := e.workspace.MaxTimeout
	if timeoutArg := number(r.Args, "timeout_sec"); timeoutArg > 0 {
		userTimeout := time.Duration(timeoutArg) * time.Second
		if userTimeout < timeout {
			timeout = userTimeout
		}
	}

	workDir := e.workspace.Root
	if cwd := text(r.Args, "cwd"); cwd != "" {
		resolved, err := e.workspace.ResolvePath(cwd)
		if err != nil {
			return Outcome{Error: fmt.Sprintf("invalid cwd: %v", err)}
		}
		workDir = resolved
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, "sh", "-c", cmdStr)
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Do not inherit gateway provider credentials or operator tokens.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + e.workspace.Root, "TMPDIR=" + e.workspace.Root}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second

	var stdout, stderr bytes.Buffer
	const maxOutput = 512 << 10 // 512 KiB max output buffer
	cmd.Stdout = &boundedWriter{b: &stdout, max: maxOutput}
	cmd.Stderr = &boundedWriter{b: &stderr, max: maxOutput}

	startTime := time.Now()
	err := cmd.Run()
	duration := time.Since(startTime)

	exitCode := 0
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			// Kill entire process group on timeout
			if cmd.Process != nil {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return Outcome{
				Status: 504,
				Error:  fmt.Sprintf("command timed out after %s", timeout),
			}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return Outcome{
				Status: 500,
				Error:  fmt.Sprintf("executing command: %v", err),
			}
		}
	}

	resPayload := map[string]any{
		"exit_code":   exitCode,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"duration_ms": duration.Milliseconds(),
	}
	body, _ := json.Marshal(resPayload)
	status := 200
	if exitCode != 0 {
		status = 400
	}
	return Outcome{Status: status, Body: body}
}

func (e *ShellExecutor) readFile(_ context.Context, r Request) Outcome {
	p := text(r.Args, "path")
	if p == "" {
		return Outcome{Error: "path is required"}
	}
	resolved, err := e.workspace.ResolvePath(p)
	if err != nil {
		return Outcome{Error: err.Error()}
	}

	rel, err := filepath.Rel(e.workspace.Root, resolved)
	if err != nil {
		return Outcome{Status: 400, Error: err.Error()}
	}
	f, err := e.workspace.root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return Outcome{Status: 404, Error: "file not found"}
		}
		return Outcome{Status: 500, Error: fmt.Sprintf("opening file: %v", err)}
	}
	defer f.Close()

	const maxRead = 1 << 20 // 1 MiB limit
	data, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("reading file: %v", err)}
	}
	truncated := false
	if len(data) > maxRead {
		data = data[:maxRead]
		truncated = true
	}

	resPayload := map[string]any{
		"path":      p,
		"content":   string(data),
		"size":      len(data),
		"truncated": truncated,
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

func (e *ShellExecutor) writeFile(_ context.Context, r Request) Outcome {
	if e.workspace.ReadOnly {
		return Outcome{Status: 403, Error: "workspace is configured as read-only"}
	}
	p := text(r.Args, "path")
	if p == "" {
		return Outcome{Error: "path is required"}
	}
	contentStr := text(r.Args, "content")
	contentEncoding := text(r.Args, "encoding")

	var data []byte
	var err error
	if contentEncoding == "base64" {
		data, err = base64.StdEncoding.DecodeString(contentStr)
		if err != nil {
			return Outcome{Error: "invalid base64 content"}
		}
	} else {
		data = []byte(contentStr)
	}

	if len(data) > 2<<20 { // 2 MiB limit
		return Outcome{Error: "file content exceeds 2 MiB limit"}
	}

	resolved, err := e.workspace.ResolvePath(p)
	if err != nil {
		return Outcome{Error: err.Error()}
	}

	rel, err := filepath.Rel(e.workspace.Root, resolved)
	if err != nil {
		return Outcome{Status: 400, Error: err.Error()}
	}
	dir := filepath.Dir(rel)
	if err := e.workspace.root.MkdirAll(dir, 0755); err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("creating directory: %v", err)}
	}

	// Anchor the parent directory while publishing the temporary file.
	parent, err := e.workspace.root.OpenRoot(dir)
	if err != nil {
		return Outcome{Status: 400, Error: err.Error()}
	}
	defer parent.Close()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Outcome{Status: 500, Error: "creating random file name failed"}
	}
	tmpName := fmt.Sprintf(".circuit-write-%x", random)
	tmpFile, err := parent.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("creating temp file: %v", err)}
	}
	defer parent.Remove(tmpName)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return Outcome{Status: 500, Error: fmt.Sprintf("writing data: %v", err)}
	}
	if err := tmpFile.Close(); err != nil {
		return Outcome{Status: 500, Error: fmt.Sprintf("closing temp file: %v", err)}
	}

	overwrite, _ := r.Args["overwrite"].(bool)
	if overwrite {
		err = parent.Rename(tmpName, filepath.Base(rel))
	} else {
		// Link publishes atomically without replacing a concurrently created file.
		err = parent.Link(tmpName, filepath.Base(rel))
	}
	if err != nil {
		if os.IsExist(err) {
			return Outcome{Status: 409, Error: "file exists; resubmit with overwrite: true for operator approval"}
		}
		return Outcome{Status: 500, Error: fmt.Sprintf("atomically replacing file: %v", err)}
	}

	resPayload := map[string]any{
		"path":    p,
		"bytes":   len(data),
		"written": true,
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

func (e *ShellExecutor) deleteFile(_ context.Context, r Request) Outcome {
	if e.workspace.ReadOnly {
		return Outcome{Status: 403, Error: "workspace is configured as read-only"}
	}
	p := text(r.Args, "path")
	if p == "" {
		return Outcome{Error: "path is required"}
	}
	resolved, err := e.workspace.ResolvePath(p)
	if err != nil {
		return Outcome{Error: err.Error()}
	}
	if resolved == e.workspace.Root {
		return Outcome{Status: 403, Error: "cannot delete workspace root"}
	}

	recursive := false
	if recVal, ok := r.Args["recursive"].(bool); ok {
		recursive = recVal
	}
	rel, err := filepath.Rel(e.workspace.Root, resolved)
	if err != nil {
		return Outcome{Status: 400, Error: err.Error()}
	}

	if recursive {
		err = e.workspace.root.RemoveAll(rel)
	} else {
		err = e.workspace.root.Remove(rel)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return Outcome{Status: 404, Error: "file not found"}
		}
		return Outcome{Status: 500, Error: fmt.Sprintf("deleting file: %v", err)}
	}

	resPayload := map[string]any{
		"path":    p,
		"deleted": true,
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

type DirEntryInfo struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

func (e *ShellExecutor) listDir(_ context.Context, r Request) Outcome {
	p := text(r.Args, "path")
	resolved, err := e.workspace.ResolvePath(p)
	if err != nil {
		return Outcome{Error: err.Error()}
	}

	rel, err := filepath.Rel(e.workspace.Root, resolved)
	if err != nil {
		return Outcome{Status: 400, Error: err.Error()}
	}
	f, err := e.workspace.root.Open(rel)
	if err != nil {
		return Outcome{Status: 400, Error: err.Error()}
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		if os.IsNotExist(err) {
			return Outcome{Status: 404, Error: "directory not found"}
		}
		return Outcome{Status: 500, Error: fmt.Sprintf("reading directory: %v", err)}
	}

	results := []DirEntryInfo{}
	for _, entry := range entries {
		info, err := entry.Info()
		size := int64(0)
		modTime := ""
		if err == nil {
			size = info.Size()
			modTime = info.ModTime().Format(time.RFC3339)
		}
		results = append(results, DirEntryInfo{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
			Size:    size,
			ModTime: modTime,
		})
	}

	resPayload := map[string]any{
		"path":    p,
		"entries": results,
		"count":   len(results),
	}
	body, _ := json.Marshal(resPayload)
	return Outcome{Status: 200, Body: body}
}

type boundedWriter struct {
	b   *bytes.Buffer
	max int
}

func (w *boundedWriter) Write(p []byte) (n int, err error) {
	remaining := w.max - w.b.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.b.Write(p[:remaining])
		return len(p), nil
	}
	return w.b.Write(p)
}

// Ensure fs.FileInfo is used
var _ = fs.ModeDir

func literalWord(w *syntax.Word) (string, bool) {
	var b strings.Builder
	var parts func([]syntax.WordPart) bool
	parts = func(ps []syntax.WordPart) bool {
		for _, p := range ps {
			switch x := p.(type) {
			case *syntax.Lit:
				if strings.ContainsAny(x.Value, "*?[]~\\") {
					return false
				}
				b.WriteString(x.Value)
			case *syntax.SglQuoted:
				b.WriteString(x.Value)
			case *syntax.DblQuoted:
				if !parts(x.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	ok := parts(w.Parts)
	return b.String(), ok
}
