package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hgayan7/circuit/pkg/config"
	"github.com/hgayan7/circuit/pkg/safety"
	bolt "go.etcd.io/bbolt"
)

type Service struct {
	cfg       *Config
	store     *Store
	executor  Executor
	inspector *safety.Inspector
}

func NewService(c *Config, s *Store, e Executor) (*Service, error) {
	if c == nil || s == nil || e == nil {
		return nil, fmt.Errorf("config, store, and executor are required")
	}
	if err := s.Recover(); err != nil {
		return nil, err
	}
	return &Service{cfg: c, store: s, executor: e, inspector: safety.New(c.Safety)}, nil
}
func (s *Service) check(agent string, req Request) (config.ActionType, string) {
	if f := s.inspector.Arguments(req.Args); f != nil {
		return config.ActionDeny, f.Reason
	}
	action, reason, err := s.cfg.evaluate(agent, req)
	if err != nil {
		return config.ActionDeny, "Policy evaluation failed"
	}
	return action, reason
}
func (s *Service) Submit(ctx context.Context, agent, key string, req Request) (*Action, error) {
	if s.store.restorePending.Load() {
		return nil, ErrRestorePending
	}
	if !keyPattern.MatchString(key) {
		return nil, fmt.Errorf("Idempotency-Key must contain 1–128 letters, digits, dots, colons, underscores, or hyphens")
	}
	// Canonical JSON creates an owned snapshot; callers cannot mutate an approved request.
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(data) > 512<<10 {
		return nil, fmt.Errorf("action exceeds 512 KiB")
	}
	if err = json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	if err = validateRequest(&req); err != nil {
		return nil, err
	}
	if s.cfg.agent(agent) == nil {
		return nil, fmt.Errorf("unknown agent")
	}
	digest := hashRequest(agent, req)
	var a *Action
	err = s.store.update(func(tx *bolt.Tx) error {
		lookup := []byte(agent + ":" + key)
		if id := tx.Bucket(keysBucket).Get(lookup); id != nil {
			var err error
			a, err = getAction(tx, string(id))
			if err != nil {
				return err
			}
			if a.Digest != digest {
				return fmt.Errorf("idempotency key already belongs to a different action")
			}
			return expireAction(tx, a)
		}
		// Even if a client changes its key, do not duplicate an identical unresolved action.
		var existingID string
		if err := tx.Bucket(actionsBucket).ForEach(func(_, v []byte) error {
			var other Action
			if err := json.Unmarshal(v, &other); err != nil {
				return err
			}
			if other.Digest != digest {
				return nil
			}
			unresolved := other.State == "executing" || other.State == "uncertain" || ((other.State == "pending" || other.State == "approved") && other.PolicyDigest == s.cfg.digest && time.Now().Before(other.ExpiresAt))
			if unresolved {
				existingID = other.ID
			}
			return nil
		}); err != nil {
			return err
		}
		if existingID != "" {
			var err error
			a, err = getAction(tx, existingID)
			if err != nil {
				return err
			}
			return tx.Bucket(keysBucket).Put(lookup, []byte(a.ID))
		}
		now := time.Now().UTC()
		a = &Action{ID: newID(), AgentID: agent, Request: req, Digest: digest, PolicyDigest: s.cfg.digest, CreatedAt: now, ExpiresAt: now.Add(s.cfg.ttl)}
		verdict, reason := s.check(agent, req)
		a.Reason = reason
		switch verdict {
		case config.ActionDeny:
			a.State = "denied"
		case config.ActionRequireApproval:
			a.State = "pending"
		default:
			a.State = "approved"
			a.ApprovedBy = "policy"
		}
		if a.State != "denied" {
			if err := reserve(tx, s.cfg, a); err != nil {
				a.State = "denied"
				a.Reason = err.Error()
			}
		}
		if err := saveAction(tx, a, agent); err != nil {
			return err
		}
		return tx.Bucket(keysBucket).Put(lookup, []byte(a.ID))
	})
	if err != nil {
		return nil, err
	}
	if a.State == "approved" {
		return s.Execute(ctx, a.ID)
	}
	return a, nil
}
func (s *Service) Decide(ctx context.Context, id, digest, decision string) (*Action, error) {
	return s.decide(ctx, id, digest, decision, "operator")
}

func (s *Service) operator(id string, adminOnly bool) (string, error) {
	for _, operator := range s.cfg.Operators {
		if operator.ID == id && (operator.Role == "admin" || (!adminOnly && operator.Role == "reviewer")) {
			return "operator:" + id, nil
		}
	}
	return "", fmt.Errorf("operator is not authorized")
}

func (s *Service) DecideAs(ctx context.Context, id, digest, decision, operatorID string) (*Action, error) {
	actor, err := s.operator(operatorID, false)
	if err != nil {
		return nil, err
	}
	return s.decide(ctx, id, digest, decision, actor)
}

func (s *Service) decide(ctx context.Context, id, digest, decision, actor string) (*Action, error) {
	if decision == "approve" && s.store.restorePending.Load() {
		return nil, ErrRestorePending
	}
	if decision != "approve" && decision != "reject" {
		return nil, fmt.Errorf("decision must be approve or reject")
	}
	var a *Action
	err := s.store.update(func(tx *bolt.Tx) error {
		var err error
		a, err = getAction(tx, id)
		if err != nil {
			return err
		}
		if err := expireAction(tx, a); err != nil {
			return err
		}
		if a.State == "expired" {
			return nil
		}
		if a.State != "pending" {
			return fmt.Errorf("action is not pending approval")
		}
		if subtle.ConstantTimeCompare([]byte(a.Digest), []byte(digest)) != 1 {
			return fmt.Errorf("approval digest does not match the reviewed action")
		}
		if decision == "reject" {
			a.State = "rejected"
			a.Reason = "Rejected by operator"
			a.ApprovedBy = actor
			a.Reservations = nil
			return saveAction(tx, a, actor)
		}
		if a.PolicyDigest != s.cfg.digest {
			a.State = "denied"
			a.Reason = "Policy changed; submit a new action for review"
			a.Reservations = nil
			return saveAction(tx, a, actor)
		}
		verdict, reason := s.check(a.AgentID, a.Request)
		if verdict == config.ActionDeny {
			a.State = "denied"
			a.Reason = reason
			a.Reservations = nil
			return saveAction(tx, a, actor)
		}
		if err := reserve(tx, s.cfg, a); err != nil {
			a.State = "denied"
			a.Reason = err.Error()
			a.Reservations = nil
			return saveAction(tx, a, actor)
		}
		a.State = "approved"
		a.ApprovedBy = actor
		a.Reason = "Approved exact action"
		return saveAction(tx, a, actor)
	})
	if err != nil {
		return nil, err
	}
	if a.State == "approved" {
		return s.Execute(ctx, id)
	}
	return a, nil
}
func (s *Service) Execute(ctx context.Context, id string) (*Action, error) {
	if s.store.restorePending.Load() {
		return nil, ErrRestorePending
	}
	var a *Action
	claimed := false
	err := s.store.update(func(tx *bolt.Tx) error {
		var err error
		a, err = getAction(tx, id)
		if err != nil {
			return err
		}
		if err := expireAction(tx, a); err != nil {
			return err
		}
		if a.State != "approved" {
			return nil
		}
		verdict, reason := s.check(a.AgentID, a.Request)
		if a.PolicyDigest != s.cfg.digest || verdict == config.ActionDeny {
			a.State = "denied"
			a.Reason = "Policy changed or action is no longer permitted: " + reason
			a.Reservations = nil
			return saveAction(tx, a, "system")
		}
		humanApproval := a.ApprovedBy == "operator" && len(s.cfg.Operators) == 0
		if operatorID, ok := strings.CutPrefix(a.ApprovedBy, "operator:"); ok {
			_, err := s.operator(operatorID, false)
			humanApproval = err == nil
		}
		if verdict == config.ActionRequireApproval && !humanApproval {
			a.State = "pending"
			return saveAction(tx, a, "system")
		}
		if err := reserve(tx, s.cfg, a); err != nil {
			a.State = "denied"
			a.Reason = err.Error()
			a.Reservations = nil
			return saveAction(tx, a, "system")
		}
		a.State = "executing"
		a.Reason = "Execution claimed; automatic replay is disabled"
		claimed = true
		return saveAction(tx, a, "system")
	})
	if err != nil || !claimed {
		return a, err
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	callCtx = context.WithValue(callCtx, executionIDKey{}, a.ID)
	outcome := s.executor.Execute(callCtx, a.Request)
	if outcome.Error == "" && s.inspector.InspectResponses() {
		var value any
		if json.Unmarshal(outcome.Body, &value) == nil {
			if f := s.inspector.ResponseValue(value); f != nil {
				outcome.Body = nil
				outcome.Warning = "Action completed, but returned content was blocked: " + f.Reason
				outcome.Uncertain = false
			}
		}
	}
	err = s.store.update(func(tx *bolt.Tx) error {
		current, err := getAction(tx, id)
		if err != nil {
			return err
		}
		a = current
		a.Outcome = &outcome
		switch {
		case outcome.Uncertain:
			a.State = "uncertain"
			a.Reason = "Outcome requires operator reconciliation; automatic retry is disabled"
		case outcome.Error != "":
			a.State = "failed"
			a.Reason = outcome.Error
		default:
			a.State = "succeeded"
			a.Reason = "Action completed"
		}
		return saveAction(tx, a, "github")
	})
	return a, err
}
func (s *Service) Reconcile(id, digest, state, note string) (*Action, error) {
	return s.reconcile(id, digest, state, note, "operator")
}

func (s *Service) ReconcileAs(id, digest, state, note, operatorID string) (*Action, error) {
	actor, err := s.operator(operatorID, true)
	if err != nil {
		return nil, err
	}
	return s.reconcile(id, digest, state, note, actor)
}

func (s *Service) reconcile(id, digest, state, note, actor string) (*Action, error) {
	if (state != "succeeded" && state != "failed") || strings.TrimSpace(note) == "" || len(note) > 1000 {
		return nil, fmt.Errorf("reconciliation needs succeeded/failed and a note of at most 1,000 characters")
	}
	var a *Action
	err := s.store.update(func(tx *bolt.Tx) error {
		var err error
		a, err = getAction(tx, id)
		if err != nil {
			return err
		}
		if a.State != "uncertain" || a.Digest != digest {
			return fmt.Errorf("only the exact uncertain action can be reconciled")
		}
		a.State = state
		a.Reason = "Operator reconciled: " + note
		return saveAction(tx, a, actor)
	})
	return a, err
}
