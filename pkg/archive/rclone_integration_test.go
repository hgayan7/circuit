package archive_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/hgayan7/circuit/pkg/archive"
	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/stretchr/testify/require"
)

func TestRcloneS3Recovery(t *testing.T) {
	if os.Getenv("CIRCUIT_ARCHIVE_S3_FIXTURE") != "1" {
		t.Skip("isolated Docker S3 fixture only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := "/fixture"
	source := filepath.Join(root, "source")
	require.NoError(t, os.MkdirAll(source, 0700))
	plain := filepath.Join(root, "original.db")
	store, err := gateway.OpenStore(plain)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	name := "circuit-20261003T000000Z-0123456789abcdef.db.age"
	path := filepath.Join(source, name)
	in, err := os.Open(plain)
	require.NoError(t, err)
	require.NoError(t, gateway.SaveEncryptedSnapshot(in, path, identity.Recipient().String(), 1<<30))
	require.NoError(t, in.Close())
	backend, err := archive.NewRclone("/fixture/storage.conf", "backup:fixture/circuit")
	require.NoError(t, err)
	receipts, err := archive.Sync(ctx, source, backend)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	// Remove disposable local state and archive before retrieving independent storage.
	require.NoError(t, os.Remove(plain))
	require.NoError(t, os.Remove(path))
	recovered := filepath.Join(root, "retrieved.age")
	cmd := exec.CommandContext(ctx, "rclone", "--config", "/fixture/storage.conf", "copyto", "backup:fixture/circuit/"+name, recovered)
	require.NoError(t, cmd.Run())
	data, err := os.ReadFile(recovered)
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	require.Equal(t, receipts[0].SHA256, hex.EncodeToString(hash[:]))
	restored := filepath.Join(root, "restored.db")
	require.NoError(t, gateway.RestoreEncryptedSnapshot(recovered, restored, identity.String()))
	store, err = gateway.OpenStore(restored)
	require.NoError(t, err)
	require.ErrorIs(t, store.Check(), gateway.ErrRestorePending)
	require.NoError(t, store.Close())
	require.NoError(t, os.WriteFile(path, data, 0600))
	require.Error(t, backend.Verify(ctx, name, "incorrect-digest", receipts[0].Bytes))
	require.Error(t, backend.Verify(ctx, name, receipts[0].SHA256, receipts[0].Bytes-1))
}
