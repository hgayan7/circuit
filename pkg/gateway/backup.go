package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (s *Store) Check() error {
	if s.unavailable.Load() {
		return ErrStorageUnavailable
	}
	if s.restorePending.Load() {
		return ErrRestorePending
	}
	return s.db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{actionsBucket, keysBucket, eventsBucket} {
			if tx.Bucket(name) == nil {
				return fmt.Errorf("missing state bucket")
			}
		}
		return nil
	})
}

func verifyState(tx *bolt.Tx) error {
	var structural error
	for err := range tx.Check() {
		if structural == nil {
			structural = err
		}
	}
	if structural != nil {
		return fmt.Errorf("database structure is invalid")
	}
	if metadata := tx.Bucket([]byte("recovery")); metadata != nil {
		pending := string(metadata.Get([]byte("restore_pending")))
		if pending != "true" && pending != "false" {
			return fmt.Errorf("invalid restore barrier metadata")
		}
	}
	for _, name := range [][]byte{actionsBucket, keysBucket, eventsBucket} {
		if tx.Bucket(name) == nil {
			return fmt.Errorf("missing state bucket")
		}
	}
	if err := tx.Bucket(actionsBucket).ForEach(func(k, _ []byte) error {
		a, err := getAction(tx, string(k))
		if err != nil {
			return err
		}
		switch a.State {
		case "pending", "approved", "executing", "uncertain", "succeeded", "failed", "denied", "rejected", "expired":
			return nil
		default:
			return fmt.Errorf("invalid stored action state")
		}
	}); err != nil {
		return err
	}
	if err := tx.Bucket(keysBucket).ForEach(func(_, v []byte) error { _, err := getAction(tx, string(v)); return err }); err != nil {
		return err
	}
	return tx.Bucket(eventsBucket).ForEach(func(_, v []byte) error {
		var event Event
		if err := json.Unmarshal(v, &event); err != nil {
			return fmt.Errorf("invalid stored event")
		}
		_, err := getAction(tx, event.ActionID)
		return err
	})
}

// VerifySnapshot never initializes buckets or recovers actions in the input file.
func VerifySnapshot(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 8192 {
		return fmt.Errorf("snapshot must be an existing regular database file")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("cannot open snapshot")
	}
	defer db.Close()
	return db.View(verifyState)
}

func removeTemp(f *os.File) { name := f.Name(); f.Close(); os.Remove(name) }

func (s *Store) backupTemp() (*os.File, error) {
	f, err := os.CreateTemp(filepath.Dir(s.db.Path()), ".circuit-backup-*")
	if err != nil {
		return nil, err
	}
	err = s.db.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(f); return err })
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = VerifySnapshot(f.Name())
	}
	if err != nil {
		removeTemp(f)
		return nil, err
	}
	return f, nil
}

// RestoreSnapshot publishes a verified private copy without replacing existing state.
// Start the gateway with the restored path only after stopping the original process.
func RestoreSnapshot(source, destination string) error {
	if err := VerifySnapshot(source); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	return saveSnapshot(in, destination, 1<<30, true)
}

// SaveSnapshot bounds and verifies downloaded bytes before exclusive publication.
func SaveSnapshot(reader io.Reader, destination string, maxBytes int64) error {
	return saveSnapshot(reader, destination, maxBytes, false)
}
func saveSnapshot(reader io.Reader, destination string, maxBytes int64, restored bool) error {
	if maxBytes <= 0 {
		return fmt.Errorf("snapshot byte limit must be positive")
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".circuit-restore-*")
	if err != nil {
		return err
	}
	defer removeTemp(f)
	written, err := io.Copy(f, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return err
	}
	if written > maxBytes {
		return fmt.Errorf("snapshot exceeds byte limit")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = VerifySnapshot(f.Name()); err != nil {
		return err
	}
	if restored {
		db, err := bolt.Open(f.Name(), 0600, &bolt.Options{Timeout: time.Second})
		if err != nil {
			return err
		}
		err = db.Update(func(tx *bolt.Tx) error {
			metadata, err := tx.CreateBucketIfNotExists([]byte("recovery"))
			if err != nil {
				return err
			}
			return metadata.Put([]byte("restore_pending"), []byte("true"))
		})
		closeErr := db.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err = os.Link(f.Name(), destination); err != nil {
		return fmt.Errorf("cannot publish snapshot (destination must not exist): %w", err)
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// AcknowledgeRestore requires exclusive offline access and stores recovery evidence.
// Restoring an old snapshot cannot know about writes committed after that snapshot.
func AcknowledgeRestore(path, note string) error {
	if len(note) < 20 || len(note) > 1000 {
		return fmt.Errorf("restore acknowledgment needs 20-1000 characters of provider reconciliation evidence")
	}
	if err := VerifySnapshot(path); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("stop the gateway before acknowledging restore")
	}
	defer db.Close()
	return db.Update(func(tx *bolt.Tx) error {
		metadata := tx.Bucket([]byte("recovery"))
		if metadata == nil || string(metadata.Get([]byte("restore_pending"))) != "true" {
			return fmt.Errorf("state is not awaiting restore acknowledgment")
		}
		evidence, _ := json.Marshal(map[string]any{"note": note, "at": time.Now().UTC(), "actor": "offline-state-owner"})
		if err := metadata.Put([]byte("restore_acknowledgment"), evidence); err != nil {
			return err
		}
		return metadata.Put([]byte("restore_pending"), []byte("false"))
	})
}
