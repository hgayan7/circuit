package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// CommTarget defines a governed communication or productivity service (chat, email, ticketing, docs).
type CommTarget struct {
	ID                         string   `yaml:"id" json:"id"`
	Kind                       string   `yaml:"kind" json:"kind"` // "chat", "email", "ticket", "docs"
	AllowedChannels            []string `yaml:"allowed_channels,omitempty" json:"allowed_channels,omitempty"`
	InternalDomains            []string `yaml:"internal_domains,omitempty" json:"internal_domains,omitempty"`
	MaxRecipients              int      `yaml:"max_recipients,omitempty" json:"max_recipients,omitempty"`
	RequireApprovalForExternal bool     `yaml:"require_approval_for_external,omitempty" json:"require_approval_for_external,omitempty"`

	simMu       sync.RWMutex
	messages    []map[string]any
	emails      []map[string]any
	tickets     map[string]map[string]any
	documents   map[string]map[string]any
	ticketSeq   int
	documentSeq int
}

// NewCommTarget creates a CommTarget for testing/simulation or integration.
func NewCommTarget(id, kind string, allowedChannels, internalDomains []string, maxRecipients int, reqApprovalExt bool) (*CommTarget, error) {
	if id == "" {
		return nil, fmt.Errorf("communication target ID is required")
	}
	if maxRecipients <= 0 {
		maxRecipients = 50
	}

	return &CommTarget{
		ID:                         id,
		Kind:                       kind,
		AllowedChannels:            allowedChannels,
		InternalDomains:            internalDomains,
		MaxRecipients:              maxRecipients,
		RequireApprovalForExternal: reqApprovalExt,
		messages:                   make([]map[string]any, 0),
		emails:                     make([]map[string]any, 0),
		tickets:                    make(map[string]map[string]any),
		documents:                  make(map[string]map[string]any),
	}, nil
}

// HasExternalRecipient returns true if any recipient address does not belong to internalDomains.
func (c *CommTarget) HasExternalRecipient(recipients []string) bool {
	if len(c.InternalDomains) == 0 {
		return false
	}
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		addr, err := mail.ParseAddress(r)
		email := r
		if err == nil {
			email = addr.Address
		}
		parts := strings.Split(email, "@")
		if len(parts) != 2 {
			return true // malformed or unqualified address treated as external/untrusted
		}
		domain := strings.ToLower(parts[1])
		internal := false
		for _, idom := range c.InternalDomains {
			if domain == strings.ToLower(idom) || strings.HasSuffix(domain, "."+strings.ToLower(idom)) {
				internal = true
				break
			}
		}
		if !internal {
			return true
		}
	}
	return false
}

// CommExecutor executes governed communication actions on a CommTarget.
type CommExecutor struct {
	target *CommTarget
}

// NewCommExecutor creates a CommExecutor.
func NewCommExecutor(target *CommTarget) *CommExecutor {
	return &CommExecutor{target: target}
}

func (e *CommExecutor) Execute(ctx context.Context, r Request) Outcome {
	return simulationOutcome(e.execute(ctx, r))
}

func (e *CommExecutor) execute(ctx context.Context, r Request) Outcome {
	switch r.Operation {
	case "send_message":
		return e.sendMessage(ctx, r)
	case "send_email":
		return e.sendEmail(ctx, r)
	case "create_ticket":
		return e.createTicket(ctx, r)
	case "update_ticket":
		return e.updateTicket(ctx, r)
	case "publish_document":
		return e.publishDocument(ctx, r)
	default:
		return Outcome{Error: fmt.Sprintf("unsupported communication operation %q", r.Operation)}
	}
}

func (e *CommExecutor) sendMessage(ctx context.Context, r Request) Outcome {
	channel := text(r.Args, "channel")
	message := text(r.Args, "message")
	if channel == "" {
		return Outcome{Error: "channel is required"}
	}
	if message == "" {
		return Outcome{Error: "message is required"}
	}

	if len(e.target.AllowedChannels) > 0 {
		allowed := false
		for _, ac := range e.target.AllowedChannels {
			if strings.EqualFold(ac, channel) {
				allowed = true
				break
			}
		}
		if !allowed {
			return Outcome{Status: 403, Error: fmt.Sprintf("channel %q is not in the allowed channels list for target %s", channel, e.target.ID)}
		}
	}

	e.target.simMu.Lock()
	defer e.target.simMu.Unlock()

	entry := map[string]any{
		"channel":   channel,
		"message":   message,
		"target":    e.target.ID,
		"timestamp": time.Now().UTC(),
	}
	e.target.messages = append(e.target.messages, entry)

	resp := map[string]any{
		"channel":   channel,
		"target":    e.target.ID,
		"status":    "sent",
		"timestamp": entry["timestamp"],
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}

func (e *CommExecutor) sendEmail(ctx context.Context, r Request) Outcome {
	subject := text(r.Args, "subject")
	content := text(r.Args, "body")
	toRaw, ok := r.Args["to"]
	if !ok {
		return Outcome{Error: "to (recipients) is required"}
	}
	if subject == "" {
		return Outcome{Error: "subject is required"}
	}
	if content == "" {
		return Outcome{Error: "body is required"}
	}

	var recipients []string
	switch v := toRaw.(type) {
	case string:
		for _, item := range strings.Split(v, ",") {
			if s := strings.TrimSpace(item); s != "" {
				recipients = append(recipients, s)
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				recipients = append(recipients, strings.TrimSpace(s))
			}
		}
	case []string:
		recipients = v
	default:
		return Outcome{Error: "to must be a recipient string or array of strings"}
	}

	if len(recipients) == 0 {
		return Outcome{Error: "at least one recipient address is required"}
	}
	if len(recipients) > e.target.MaxRecipients {
		return Outcome{Status: 400, Error: fmt.Sprintf("recipient count %d exceeds target limit %d", len(recipients), e.target.MaxRecipients)}
	}

	e.target.simMu.Lock()
	defer e.target.simMu.Unlock()

	entry := map[string]any{
		"to":        recipients,
		"subject":   subject,
		"body":      content,
		"target":    e.target.ID,
		"timestamp": time.Now().UTC(),
	}
	e.target.emails = append(e.target.emails, entry)

	resp := map[string]any{
		"recipients_count": len(recipients),
		"subject":          subject,
		"target":           e.target.ID,
		"status":           "sent",
		"timestamp":        entry["timestamp"],
	}
	body, _ := json.Marshal(resp)
	return Outcome{Status: 200, Body: body}
}

func (e *CommExecutor) createTicket(ctx context.Context, r Request) Outcome {
	title := text(r.Args, "title")
	description := text(r.Args, "description")
	project := text(r.Args, "project")
	if title == "" {
		return Outcome{Error: "title is required"}
	}
	if project == "" {
		project = "DEFAULT"
	}

	e.target.simMu.Lock()
	defer e.target.simMu.Unlock()

	e.target.ticketSeq++
	ticketKey := fmt.Sprintf("%s-%d", strings.ToUpper(project), e.target.ticketSeq)
	ticket := map[string]any{
		"key":         ticketKey,
		"project":     project,
		"title":       title,
		"description": description,
		"status":      "open",
		"created_at":  time.Now().UTC(),
	}
	e.target.tickets[ticketKey] = ticket

	body, _ := json.Marshal(ticket)
	return Outcome{Status: 200, Body: body}
}

func (e *CommExecutor) updateTicket(ctx context.Context, r Request) Outcome {
	ticketKey := text(r.Args, "key")
	if ticketKey == "" {
		return Outcome{Error: "key is required"}
	}

	e.target.simMu.Lock()
	defer e.target.simMu.Unlock()

	ticket, ok := e.target.tickets[ticketKey]
	if !ok {
		return Outcome{Status: 404, Error: fmt.Sprintf("ticket %q not found", ticketKey)}
	}

	if st := text(r.Args, "status"); st != "" {
		ticket["status"] = st
	}
	if comment := text(r.Args, "comment"); comment != "" {
		comments, _ := ticket["comments"].([]string)
		ticket["comments"] = append(comments, comment)
	}
	ticket["updated_at"] = time.Now().UTC()

	body, _ := json.Marshal(ticket)
	return Outcome{Status: 200, Body: body}
}

func (e *CommExecutor) publishDocument(ctx context.Context, r Request) Outcome {
	title := text(r.Args, "title")
	content := text(r.Args, "content")
	if title == "" {
		return Outcome{Error: "title is required"}
	}
	if content == "" {
		return Outcome{Error: "content is required"}
	}

	e.target.simMu.Lock()
	defer e.target.simMu.Unlock()

	e.target.documentSeq++
	docID := fmt.Sprintf("doc-%d", e.target.documentSeq)
	doc := map[string]any{
		"id":           docID,
		"title":        title,
		"content":      content,
		"published_at": time.Now().UTC(),
		"published_by": "circuit",
	}
	e.target.documents[docID] = doc

	body, _ := json.Marshal(doc)
	return Outcome{Status: 200, Body: body}
}
