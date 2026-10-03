// Package archive verifies encrypted backup copies through replaceable storage backends.
package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var Name = regexp.MustCompile(`^circuit-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{16}\.db\.age$`)

const MaxBytes int64 = (1 << 30) + (2 << 20)

type Backend interface {
	Put(context.Context, string, string) error
	Verify(context.Context, string, string, int64) error
}
type Receipt struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func Inspect(path string) (Receipt, error) {
	if !Name.MatchString(filepath.Base(path)) {
		return Receipt{}, fmt.Errorf("not an owned archive name")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 32 || info.Size() > MaxBytes {
		return Receipt{}, fmt.Errorf("archive must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Receipt{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return Receipt{}, fmt.Errorf("archive changed before opening")
	}
	header := make([]byte, len("age-encryption.org/v1\n"))
	if _, err := io.ReadFull(f, header); err != nil || string(header) != "age-encryption.org/v1\n" {
		return Receipt{}, fmt.Errorf("only encrypted age archives may be uploaded")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return Receipt{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxBytes+1))
	if err != nil || n != info.Size() {
		return Receipt{}, fmt.Errorf("archive changed or cannot be read")
	}
	return Receipt{Name: info.Name(), SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, nil
}

// Sync verifies every published archive remotely. It never deletes remote data.
func Sync(ctx context.Context, dir string, backend Backend) ([]Receipt, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if Name.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no encrypted archive is available")
	}
	var receipts []Receipt
	for _, name := range names {
		path := filepath.Join(dir, name)
		receipt, err := Inspect(path)
		if err != nil {
			return nil, err
		}
		if err := backend.Put(ctx, path, name); err != nil {
			return nil, fmt.Errorf("archive upload failed")
		}
		if err := backend.Verify(ctx, name, receipt.SHA256, receipt.Bytes); err != nil {
			return nil, fmt.Errorf("remote archive verification failed")
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}
