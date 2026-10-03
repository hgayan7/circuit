package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// VerifyWebhookSignature validates GitHub's HMAC-SHA256 signature from the X-Hub-Signature-256 header.
func VerifyWebhookSignature(secret, signatureHeader string, body []byte) bool {
	if secret == "" || signatureHeader == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	expectedHex := strings.TrimPrefix(signatureHeader, prefix)
	expectedSig, err := hex.DecodeString(expectedHex)
	if err != nil || len(expectedSig) != 32 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	actualSig := mac.Sum(nil)
	return subtle.ConstantTimeCompare(actualSig, expectedSig) == 1
}

type WebhookRepo struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
}

type WebhookPR struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
}

type WebhookIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

type WebhookCommit struct {
	ID       string   `json:"id"`
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

type WebhookPayload struct {
	Action      string          `json:"action"`
	Ref         string          `json:"ref"`
	RefType     string          `json:"ref_type"`
	Repository  WebhookRepo     `json:"repository"`
	PullRequest *WebhookPR      `json:"pull_request,omitempty"`
	Issue       *WebhookIssue   `json:"issue,omitempty"`
	Commits     []WebhookCommit `json:"commits,omitempty"`
	HeadCommit  *WebhookCommit  `json:"head_commit,omitempty"`
}

type WebhookReconcileResult struct {
	Event          string   `json:"event"`
	MatchedActions []string `json:"matched_actions"`
	Reconciled     int      `json:"reconciled"`
}

// ReconcileWebhook inspects a verified GitHub webhook event and reconciles
// any matching in-flight or uncertain actions in the gateway store.
func (s *Service) ReconcileWebhook(ctx context.Context, eventType string, body []byte) (*WebhookReconcileResult, error) {
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parsing webhook JSON payload: %w", err)
	}

	repo := strings.ToLower(payload.Repository.FullName)
	result := &WebhookReconcileResult{
		Event:          eventType,
		MatchedActions: []string{},
	}
	if repo == "" {
		return result, nil
	}

	err := s.store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(actionsBucket)
		if b == nil {
			return nil
		}

		return b.ForEach(func(k, v []byte) error {
			var a Action
			if err := json.Unmarshal(v, &a); err != nil {
				return nil
			}
			// Only reconcile actions that are in an uncertain state
			if a.State != "uncertain" {
				return nil
			}
			if strings.ToLower(a.Request.Repository) != repo {
				return nil
			}

			matched := false
			var newOutcome Outcome
			newState := ""
			newReason := ""

			switch a.Request.Operation {
			case "merge_pr":
				if eventType == "pull_request" && payload.PullRequest != nil {
					prNum := number(a.Request.Args, "number")
					if prNum == payload.PullRequest.Number {
						matched = true
						if payload.PullRequest.Merged {
							newState = "succeeded"
							newReason = fmt.Sprintf("Reconciled via webhook: PR #%d confirmed merged", prNum)
							bodyData, _ := json.Marshal(map[string]any{
								"merged":         true,
								"sha":            payload.PullRequest.MergeCommitSHA,
								"reconciled_by":  "webhook",
							})
							newOutcome = Outcome{Status: 200, Body: bodyData}
						} else if payload.Action == "closed" {
							newState = "failed"
							newReason = fmt.Sprintf("Reconciled via webhook: PR #%d closed without merging", prNum)
							newOutcome = Outcome{Status: 400, Error: newReason}
						}
					}
				}

			case "create_pr":
				if eventType == "pull_request" && payload.PullRequest != nil {
					headArg := text(a.Request.Args, "head")
					titleArg := text(a.Request.Args, "title")
					if (headArg != "" && (payload.PullRequest.Head.Ref == headArg || strings.HasSuffix(payload.PullRequest.Head.Ref, "/"+headArg))) ||
						(titleArg != "" && payload.PullRequest.Title == titleArg) {
						matched = true
						newState = "succeeded"
						newReason = fmt.Sprintf("Reconciled via webhook: PR #%d confirmed opened", payload.PullRequest.Number)
						bodyData, _ := json.Marshal(map[string]any{
							"number":        payload.PullRequest.Number,
							"html_url":      payload.PullRequest.HTMLURL,
							"reconciled_by": "webhook",
						})
						newOutcome = Outcome{Status: 201, Body: bodyData}
					}
				}

			case "create_branch":
				branchArg := text(a.Request.Args, "branch")
				if branchArg != "" {
					isBranchCreate := (eventType == "create" && payload.RefType == "branch" && payload.Ref == branchArg) ||
						(eventType == "push" && payload.Ref == "refs/heads/"+branchArg)
					if isBranchCreate {
						matched = true
						newState = "succeeded"
						newReason = fmt.Sprintf("Reconciled via webhook: branch %s confirmed created", branchArg)
						bodyData, _ := json.Marshal(map[string]any{
							"ref":           "refs/heads/" + branchArg,
							"reconciled_by": "webhook",
						})
						newOutcome = Outcome{Status: 201, Body: bodyData}
					}
				}

			case "put_file":
				branchArg := text(a.Request.Args, "branch")
				pathArg := text(a.Request.Args, "path")
				if eventType == "push" && branchArg != "" && payload.Ref == "refs/heads/"+branchArg {
					fileChanged := false
					checkFile := func(files ...string) {
						for _, f := range files {
							if f == pathArg {
								fileChanged = true
							}
						}
					}
					for _, c := range payload.Commits {
						checkFile(c.Added...)
						checkFile(c.Modified...)
					}
					if payload.HeadCommit != nil {
						checkFile(payload.HeadCommit.Added...)
						checkFile(payload.HeadCommit.Modified...)
					}
					if fileChanged {
						matched = true
						newState = "succeeded"
						newReason = fmt.Sprintf("Reconciled via webhook: file %s confirmed written on branch %s", pathArg, branchArg)
						bodyData, _ := json.Marshal(map[string]any{
							"path":          pathArg,
							"reconciled_by": "webhook",
						})
						newOutcome = Outcome{Status: 200, Body: bodyData}
					}
				}

			case "create_issue":
				if eventType == "issues" && (payload.Action == "opened" || payload.Action == "") && payload.Issue != nil {
					titleArg := text(a.Request.Args, "title")
					if titleArg != "" && payload.Issue.Title == titleArg {
						matched = true
						newState = "succeeded"
						newReason = fmt.Sprintf("Reconciled via webhook: issue #%d confirmed created", payload.Issue.Number)
						bodyData, _ := json.Marshal(map[string]any{
							"number":        payload.Issue.Number,
							"html_url":      payload.Issue.HTMLURL,
							"reconciled_by": "webhook",
						})
						newOutcome = Outcome{Status: 201, Body: bodyData}
					}
				}

			case "update_issue":
				if eventType == "issues" && payload.Issue != nil {
					issueNum := number(a.Request.Args, "number")
					if issueNum == payload.Issue.Number {
						matched = true
						newState = "succeeded"
						newReason = fmt.Sprintf("Reconciled via webhook: issue #%d updated (%s)", issueNum, payload.Action)
						bodyData, _ := json.Marshal(map[string]any{
							"number":        payload.Issue.Number,
							"state":         payload.Issue.State,
							"reconciled_by": "webhook",
						})
						newOutcome = Outcome{Status: 200, Body: bodyData}
					}
				}
			}

			if matched && newState != "" {
				a.State = newState
				a.Reason = newReason
				a.Outcome = &newOutcome
				if err := saveAction(tx, &a, "webhook"); err != nil {
					return err
				}
				result.MatchedActions = append(result.MatchedActions, a.ID)
				result.Reconciled++
			}
			return nil
		})
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}
