package gateway

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

// SaveEncryptedSnapshot verifies plaintext in private temporary storage before
// publishing an age-encrypted archive. Put TMPDIR on tmpfs for backup workers.
func SaveEncryptedSnapshot(reader io.Reader, destination, recipient string, maxBytes int64) error {
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("invalid age X25519 recipient")
	}
	dir, err := os.MkdirTemp("", "circuit-snapshot-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	plain := filepath.Join(dir, "verified.db")
	if err := SaveSnapshot(reader, plain, maxBytes); err != nil {
		return err
	}
	in, err := os.Open(plain)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".circuit-encrypted-*")
	if err != nil {
		return err
	}
	defer removeTemp(out)
	encrypted, err := age.Encrypt(out, r)
	if err != nil {
		return err
	}
	if _, err = io.Copy(encrypted, in); err != nil {
		return err
	}
	if err = encrypted.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = os.Link(out.Name(), destination); err != nil {
		return fmt.Errorf("cannot publish encrypted snapshot: %w", err)
	}
	d, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// RestoreEncryptedSnapshot authenticates the entire stream before publishing
// verified state with the same mandatory restore barrier as plaintext snapshots.
func RestoreEncryptedSnapshot(source, destination, identity string) error {
	i, err := age.ParseX25519Identity(identity)
	if err != nil {
		return fmt.Errorf("invalid age X25519 identity")
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > (1<<30)+(2<<20) {
		return fmt.Errorf("encrypted snapshot must be a bounded regular file")
	}
	plain, err := age.Decrypt(f, i)
	if err != nil {
		return fmt.Errorf("cannot decrypt snapshot")
	}
	return saveSnapshot(plain, destination, 1<<30, true)
}
