package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	objects          map[string][]byte
	failPut, corrupt bool
}

func (s *memoryStore) Put(_ context.Context, path, key string) error {
	if s.failPut {
		return fmt.Errorf("secret provider detail")
	}
	data, err := os.ReadFile(path)
	s.objects[key] = data
	return err
}
func (s *memoryStore) Verify(_ context.Context, key, digest string, size int64) error {
	data := s.objects[key]
	h := sha256.Sum256(data)
	if s.corrupt || int64(len(data)) != size || hex.EncodeToString(h[:]) != digest {
		return fmt.Errorf("bad object")
	}
	return nil
}
func TestArchivePublicationRequiresRemoteVerification(t *testing.T) {
	dir := t.TempDir()
	name := "circuit-20261003T000000Z-0123456789abcdef.db.age"
	data := []byte("age-encryption.org/v1\nfixture ciphertext bytes")
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0600))
	store := &memoryStore{objects: map[string][]byte{}}
	receipts, err := Sync(context.Background(), dir, store)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, data, store.objects[name])
	store.corrupt = true
	_, err = Sync(context.Background(), dir, store)
	require.ErrorContains(t, err, "verification failed")
	store.corrupt = false
	store.failPut = true
	_, err = Sync(context.Background(), dir, store)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret provider")
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("plaintext database must not leave the worker"), 0600))
	_, err = Sync(context.Background(), dir, store)
	require.ErrorContains(t, err, "only encrypted")
	require.NoError(t, os.Remove(filepath.Join(dir, name)))
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, name)))
	_, err = Sync(context.Background(), dir, store)
	require.Error(t, err)
}
func TestRemoteScopeAndBoundedReadback(t *testing.T) {
	for _, remote := range []string{"--config:evil", ":s3:bucket", "remote:../escape", "remote:bucket?token=secret", "/tmp/destination"} {
		_, err := NewRclone("fixture", remote)
		require.Error(t, err)
	}
	_, err := NewRclone("fixture", "backup:bucket/circuit")
	require.NoError(t, err)
	w := &boundedHash{h: sha256.New(), remaining: 3}
	_, err = w.Write([]byte("four"))
	require.Error(t, err)
	require.Equal(t, int64(3), w.remaining)
	_, err = ioCopyFixture(w)
	require.NoError(t, err)
	require.Zero(t, w.remaining)
}
func ioCopyFixture(w *boundedHash) (int, error) { return w.Write(bytes.Repeat([]byte("x"), 3)) }
