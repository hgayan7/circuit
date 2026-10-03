package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var actionsBucket = []byte("actions")
var keysBucket = []byte("idempotency")
var eventsBucket = []byte("events")

type Reservation struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}
type Action struct {
	ID           string        `json:"id"`
	AgentID      string        `json:"agent_id"`
	Request      Request       `json:"request"`
	Digest       string        `json:"digest"`
	PolicyDigest string        `json:"policy_digest"`
	State        string        `json:"state"`
	Reason       string        `json:"reason"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	ExpiresAt    time.Time     `json:"expires_at"`
	ApprovedBy   string        `json:"approved_by,omitempty"`
	Outcome      *Outcome      `json:"outcome,omitempty"`
	Reservations []Reservation `json:"reservations,omitempty"`
}
type Event struct {
	ID           uint64    `json:"id"`
	ActionID     string    `json:"action_id"`
	AgentID      string    `json:"agent_id"`
	Actor        string    `json:"actor"`
	State        string    `json:"state"`
	Reason       string    `json:"reason"`
	Digest       string    `json:"digest"`
	PolicyDigest string    `json:"policy_digest"`
	Timestamp    time.Time `json:"timestamp"`
}
type Store struct{ db *bolt.DB }

func OpenStore(path string) (*Store, error) {
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("state file must be private (chmod 600)")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{actionsBucket, keysBucket, eventsBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hashRequest(agent string, r Request) string {
	data, _ := json.Marshal(struct {
		Agent   string  `json:"agent"`
		Request Request `json:"request"`
	}{agent, r})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func getAction(tx *bolt.Tx, id string) (*Action, error) {
	data := tx.Bucket(actionsBucket).Get([]byte(id))
	if data == nil {
		return nil, fmt.Errorf("action not found")
	}
	var a Action
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	if a.ID != id || a.Digest != hashRequest(a.AgentID, a.Request) {
		return nil, fmt.Errorf("stored action integrity check failed")
	}
	return &a, nil
}
func saveAction(tx *bolt.Tx, a *Action, actor string) error {
	a.UpdatedAt = time.Now().UTC()
	encoded, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err = tx.Bucket(actionsBucket).Put([]byte(a.ID), encoded); err != nil {
		return err
	}
	seq, err := tx.Bucket(eventsBucket).NextSequence()
	if err != nil {
		return err
	}
	event := Event{ID: seq, ActionID: a.ID, AgentID: a.AgentID, Actor: actor, State: a.State, Reason: a.Reason, Digest: a.Digest, PolicyDigest: a.PolicyDigest, Timestamp: a.UpdatedAt}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, seq)
	return tx.Bucket(eventsBucket).Put(key, data)
}
func expireAction(tx *bolt.Tx, a *Action) error {
	if (a.State == "pending" || a.State == "approved") && !time.Now().Before(a.ExpiresAt) {
		a.State = "expired"
		a.Reason = "Action authorization expired"
		a.Reservations = nil
		return saveAction(tx, a, "system")
	}
	return nil
}
func (s *Store) Get(id string) (*Action, error) {
	var a *Action
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		a, err = getAction(tx, id)
		if err != nil {
			return err
		}
		return expireAction(tx, a)
	})
	return a, err
}
func (s *Store) List(agent string) ([]Action, error) {
	result := []Action{}
	err := s.db.Update(func(tx *bolt.Tx) error {
		all := []Action{}
		if err := tx.Bucket(actionsBucket).ForEach(func(_, v []byte) error {
			var a Action
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			all = append(all, a)
			return nil
		}); err != nil {
			return err
		}
		for _, a := range all {
			if err := expireAction(tx, &a); err != nil {
				return err
			}
			if agent == "" || a.AgentID == agent {
				result = append(result, a)
			}
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > 100 {
		result = result[:100]
	}
	return result, err
}

func (s *Store) Events(id string) ([]Event, error) {
	events := []Event{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(eventsBucket).ForEach(func(_, v []byte) error {
			var e Event
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
			if e.ActionID == id {
				events = append(events, e)
			}
			return nil
		})
	})
	return events, err
}

// Recover conservatively marks writes claimed before a previous process exit.
func (s *Store) Recover() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		all := []Action{}
		if err := tx.Bucket(actionsBucket).ForEach(func(_, v []byte) error {
			var a Action
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			all = append(all, a)
			return nil
		}); err != nil {
			return err
		}
		for _, a := range all {
			if a.State == "executing" {
				a.State = "uncertain"
				a.Reason = "Gateway restarted during execution; reconcile before submitting another action"
				if err := saveAction(tx, &a, "system"); err != nil {
					return err
				}
			} else if err := expireAction(tx, &a); err != nil {
				return err
			}
		}
		return nil
	})
}

func limitKey(l Limit, a *Action) string {
	key := l.ID
	switch l.Scope {
	case "agent":
		key += "/agent/" + a.AgentID
	case "repository":
		key += "/repo/" + a.Request.Repository
	case "agent_repository":
		key += "/agent/" + a.AgentID + "/repo/" + a.Request.Repository
	case "workspace":
		key += "/ws/" + a.Request.Workspace
	case "agent_workspace":
		key += "/agent/" + a.AgentID + "/ws/" + a.Request.Workspace
	case "database":
		key += "/db/" + a.Request.Database
	case "agent_database":
		key += "/agent/" + a.AgentID + "/db/" + a.Request.Database
	case "environment":
		key += "/env/" + a.Request.Environment
	case "agent_environment":
		key += "/agent/" + a.AgentID + "/env/" + a.Request.Environment
	case "channel":
		key += "/comm/" + a.Request.Channel
	case "agent_channel":
		key += "/agent/" + a.AgentID + "/comm/" + a.Request.Channel
	case "account":
		key += "/acc/" + a.Request.Account
	case "agent_account":
		key += "/agent/" + a.AgentID + "/acc/" + a.Request.Account
	}
	return key
}

// reserve checks all limits before storing anything: partial reservations cannot occur.
func reserve(tx *bolt.Tx, c *Config, a *Action) error {
	now := time.Now().UTC()
	reservations := []Reservation{}
	for _, limit := range c.Limits {
		if !member(limit.Actions, a.Request.Operation) {
			continue
		}
		key := limitKey(limit, a)
		used := 0
		cutoff := now.Add(-limit.duration)
		err := tx.Bucket(actionsBucket).ForEach(func(_, v []byte) error {
			var other Action
			if err := json.Unmarshal(v, &other); err != nil {
				return err
			}
			if other.ID == a.ID {
				return nil
			}
			if (other.State == "pending" || other.State == "approved") && !now.Before(other.ExpiresAt) {
				return nil
			}
			for _, r := range other.Reservations {
				if r.Key == key && r.At.After(cutoff) {
					used++
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if used >= limit.MaxCalls {
			return fmt.Errorf("Action budget %s exhausted (%d/%d in %s)", limit.ID, used, limit.MaxCalls, limit.Window)
		}
		reservations = append(reservations, Reservation{Key: key, At: now})
	}
	a.Reservations = reservations
	return nil
}
