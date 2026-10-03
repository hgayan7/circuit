package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetentionOnlyRemovesOwnedRegularArchives(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"circuit-20261003T000001Z-0123456789abcdef.db.age", "circuit-20261003T000002Z-0123456789abcdef.db.age", "circuit-20261003T000003Z-0123456789abcdef.db.age", "important.db.age"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600))
	}
	require.NoError(t, os.Symlink(filepath.Join(dir, "important.db.age"), filepath.Join(dir, "circuit-20261003T000000Z-0123456789abcdef.db.age")))
	require.Error(t, prune(dir, 0))
	require.NoError(t, prune(dir, 2))
	_, err := os.Stat(filepath.Join(dir, "circuit-20261003T000001Z-0123456789abcdef.db.age"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "important.db.age"))
	require.NoError(t, err)
	info, err := os.Lstat(filepath.Join(dir, "circuit-20261003T000000Z-0123456789abcdef.db.age"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}
