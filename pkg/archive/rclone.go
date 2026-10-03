package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// Rclone uses its maintained backend plugins without loading code into Circuit.
type Rclone struct{ Config, Remote string }

var remoteName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*:[A-Za-z0-9/_-]*$`)

func NewRclone(config, remote string) (*Rclone, error) {
	if config == "" || !remoteName.MatchString(remote) || strings.Contains(remote, "//") || strings.Contains(remote, "..") {
		return nil, fmt.Errorf("storage requires a config file and fixed named remote prefix")
	}
	return &Rclone{Config: config, Remote: strings.TrimRight(remote, "/")}, nil
}
func (r *Rclone) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "rclone", append([]string{"--config", r.Config, "--retries", "1", "--low-level-retries", "1", "--log-level", "ERROR"}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp"}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd
}
func (r *Rclone) key(name string) (string, error) {
	if !Name.MatchString(name) {
		return "", fmt.Errorf("invalid archive key")
	}
	separator := "/"
	if strings.HasSuffix(r.Remote, ":") {
		separator = ""
	}
	return r.Remote + separator + name, nil
}
func (r *Rclone) Put(ctx context.Context, path, name string) error {
	key, err := r.key(name)
	if err != nil {
		return err
	}
	return r.command(ctx, "copyto", path, key, "--immutable", "--checksum").Run()
}

type boundedHash struct {
	h         hash.Hash
	remaining int64
}

func (w *boundedHash) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("remote archive exceeded expected length")
	}
	n, err := w.h.Write(p)
	w.remaining -= int64(n)
	return n, err
}
func (r *Rclone) Verify(ctx context.Context, name, digest string, size int64) error {
	if size < 32 || size > MaxBytes {
		return fmt.Errorf("invalid archive size")
	}
	key, err := r.key(name)
	if err != nil {
		return err
	}
	w := &boundedHash{h: sha256.New(), remaining: size}
	cmd := r.command(ctx, "cat", key)
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot retrieve remote archive")
	}
	if w.remaining != 0 || hex.EncodeToString(w.h.Sum(nil)) != digest {
		return fmt.Errorf("remote archive checksum mismatch")
	}
	return nil
}
