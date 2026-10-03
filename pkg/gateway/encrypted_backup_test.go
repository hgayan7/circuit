package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
)

func TestEncryptedBackupRoundTripAndTampering(t *testing.T) {
	s, _ := testService(t, testConfig(t, ""), &countingExecutor{})
	a, err := s.Submit(context.Background(), "agent", "encrypted-original", prRequest())
	require.NoError(t, err)
	plain, err := s.store.backupTemp()
	require.NoError(t, err)
	defer removeTemp(plain)
	_, err = plain.Seek(0, 0)
	require.NoError(t, err)
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	dir := t.TempDir()
	archive := filepath.Join(dir, "backup.age")
	require.NoError(t, SaveEncryptedSnapshot(plain, archive, identity.Recipient().String(), 1<<30))
	info, err := os.Stat(archive)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	require.NotContains(t, string(data), a.ID)
	restoredPath := filepath.Join(dir, "restored.db")
	require.NoError(t, RestoreEncryptedSnapshot(archive, restoredPath, identity.String()))
	restored, err := OpenStore(restoredPath)
	require.NoError(t, err)
	defer restored.Close()
	require.ErrorIs(t, restored.Check(), ErrRestorePending)
	stored, err := restored.Get(a.ID)
	require.NoError(t, err)
	require.Equal(t, a.Digest, stored.Digest)
	require.Error(t, RestoreEncryptedSnapshot(archive, restoredPath, identity.String()))
	wrong, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	destination := filepath.Join(dir, "must-not-exist.db")
	require.Error(t, RestoreEncryptedSnapshot(archive, destination, wrong.String()))
	for _, damaged := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 1)} {
		require.NoError(t, os.WriteFile(archive, damaged, 0600))
		require.Error(t, RestoreEncryptedSnapshot(archive, destination, identity.String()))
		_, err := os.Stat(destination)
		require.True(t, os.IsNotExist(err))
	}
	data[len(data)-1] ^= 1
	require.NoError(t, os.WriteFile(archive, data, 0600))
	require.Error(t, RestoreEncryptedSnapshot(archive, destination, identity.String()))
}
