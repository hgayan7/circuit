package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"strings"
)

func VerifyWebhookSignature(secret, header string, body []byte) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(sig) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sig)
}

type WebhookReconcileResult struct {
	Event          string   `json:"event"`
	MatchedActions []string `json:"matched_actions"`
	Reconciled     int      `json:"reconciled"`
}

// ReconcileWebhook accepts exact merged-head evidence. Titles and changed paths
// cannot prove an approved payload executed; those outcomes need operator review.
func (s *Service) ReconcileWebhook(ctx context.Context, eventType string, body []byte) (*WebhookReconcileResult, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PR *struct {
			Number         int    `json:"number"`
			Merged         bool   `json:"merged"`
			MergeCommitSHA string `json:"merge_commit_sha"`
			Head           struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parsing webhook JSON payload: %w", err)
	}
	result := &WebhookReconcileResult{Event: eventType, MatchedActions: []string{}}
	if eventType != "pull_request" || payload.Action != "closed" || payload.PR == nil || !payload.PR.Merged || payload.PR.Head.SHA == "" || payload.PR.MergeCommitSHA == "" {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err := s.store.update(func(tx *bolt.Tx) error {
		return tx.Bucket(actionsBucket).ForEach(func(_, v []byte) error {
			var a Action
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			if a.State != "uncertain" || a.Request.Operation != "merge_pr" ||
				!strings.EqualFold(a.Request.Repository, payload.Repository.FullName) ||
				number(a.Request.Args, "number") != payload.PR.Number ||
				text(a.Request.Args, "sha") != payload.PR.Head.SHA {
				return nil
			}
			data, _ := json.Marshal(map[string]any{"merged": true, "sha": payload.PR.MergeCommitSHA, "reconciled_by": "webhook"})
			a.State = "succeeded"
			a.Reason = fmt.Sprintf("Reconciled via webhook: PR #%d confirmed merged at approved head", payload.PR.Number)
			a.Outcome = &Outcome{Status: 200, Body: data}
			if err := saveAction(tx, &a, "webhook"); err != nil {
				return err
			}
			result.MatchedActions = append(result.MatchedActions, a.ID)
			result.Reconciled++
			return nil
		})
	})
	return result, err
}
