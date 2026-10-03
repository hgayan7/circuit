package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Tokens struct {
	Admin  string
	Agents map[string]string
}
type principal struct {
	Agent string
	Admin bool
}
type identityKey struct{}
type HTTPHandler struct {
	service   *Service
	adminHash [32]byte
	agents    map[string][32]byte
	mcp       http.Handler
	mux       *http.ServeMux
}

func NewHTTPHandler(s *Service, tokens Tokens) (*HTTPHandler, error) {
	if len(tokens.Admin) < 32 {
		return nil, fmt.Errorf("admin token must contain at least 32 characters")
	}
	h := &HTTPHandler{service: s, adminHash: sha256.Sum256([]byte(tokens.Admin)), agents: map[string][32]byte{}}
	used := map[[32]byte]bool{h.adminHash: true}
	for _, a := range s.cfg.Agents {
		token := tokens.Agents[a.ID]
		hash := sha256.Sum256([]byte(token))
		if len(token) < 32 || used[hash] {
			return nil, fmt.Errorf("agent %s needs a distinct token of at least 32 characters", a.ID)
		}
		used[hash] = true
		h.agents[a.ID] = hash
	}
	servers := map[string]*mcp.Server{}
	for _, a := range s.cfg.Agents {
		servers[a.ID] = h.mcpServer(a)
	}
	h.mcp = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, _ := r.Context().Value(identityKey{}).(principal)
		return servers[p.Agent]
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	h.mux = mux
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, uiHTML)
	})
	mux.HandleFunc("GET /ui.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, uiJS)
	})
	mux.HandleFunc("POST /v1/actions", h.submit)
	mux.HandleFunc("GET /v1/actions", h.list)
	mux.HandleFunc("GET /v1/actions/{id}", h.get)
	mux.HandleFunc("GET /admin/actions", h.list)
	mux.HandleFunc("GET /admin/actions/{id}/events", h.events)
	mux.HandleFunc("POST /admin/actions/{id}/decision", h.decide)
	mux.HandleFunc("POST /admin/actions/{id}/reconcile", h.reconcile)
	mux.Handle("/mcp", h.mcp)
	return h, nil
}
func (h *HTTPHandler) authenticate(r *http.Request) (principal, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return principal{}, false
	}
	hash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(hash[:], h.adminHash[:]) == 1 {
		return principal{Admin: true}, true
	}
	for id, expected := range h.agents {
		if subtle.ConstantTimeCompare(hash[:], expected[:]) == 1 {
			return principal{Agent: id}, true
		}
	}
	return principal{}, false
}
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.URL.Path == "/" || r.URL.Path == "/ui.js" {
		h.mux.ServeHTTP(w, r)
		return
	}
	p, ok := h.authenticate(r)
	if !ok {
		writeJSON(w, 401, map[string]string{"error": "Valid Circuit bearer credential required"})
		return
	}
	adminRoute := strings.HasPrefix(r.URL.Path, "/admin/")
	if adminRoute != p.Admin {
		writeJSON(w, 403, map[string]string{"error": "Credential role cannot access this endpoint"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	h.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, p)))
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Invalid request JSON: " + err.Error()})
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "Expected one JSON object"})
		return false
	}
	return true
}
func respondAction(w http.ResponseWriter, a *Action, err error) {
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	status := 200
	switch a.State {
	case "pending", "approved", "executing":
		status = 202
	case "denied", "rejected":
		status = 403
	case "expired":
		status = 410
	case "uncertain":
		status = 202
	}
	writeJSON(w, status, a)
}
func (h *HTTPHandler) submit(w http.ResponseWriter, r *http.Request) {
	var req Request
	if !decode(w, r, &req) {
		return
	}
	p := r.Context().Value(identityKey{}).(principal)
	a, err := h.service.Submit(r.Context(), p.Agent, r.Header.Get("Idempotency-Key"), req)
	respondAction(w, a, err)
}
func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(identityKey{}).(principal)
	actions, err := h.service.store.List(p.Agent)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "Cannot read action history"})
		return
	}
	writeJSON(w, 200, actions)
}
func (h *HTTPHandler) get(w http.ResponseWriter, r *http.Request) {
	a, err := h.service.store.Get(r.PathValue("id"))
	p := r.Context().Value(identityKey{}).(principal)
	if err != nil || a.AgentID != p.Agent {
		writeJSON(w, 404, map[string]string{"error": "Action not found"})
		return
	}
	writeJSON(w, 200, a)
}
func (h *HTTPHandler) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.service.store.Events(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "Cannot read event history"})
		return
	}
	writeJSON(w, 200, events)
}
func (h *HTTPHandler) decide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Digest   string `json:"digest"`
		Decision string `json:"decision"`
	}
	if !decode(w, r, &req) {
		return
	}
	a, err := h.service.Decide(r.Context(), r.PathValue("id"), req.Digest, req.Decision)
	respondAction(w, a, err)
}
func (h *HTTPHandler) reconcile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Digest string `json:"digest"`
		State  string `json:"state"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	a, err := h.service.Reconcile(r.PathValue("id"), req.Digest, req.State, req.Note)
	respondAction(w, a, err)
}

type toolInput struct {
	Repository     string         `json:"repository" jsonschema:"Allowed owner/repository name"`
	Args           map[string]any `json:"args" jsonschema:"Operation-specific arguments"`
	IdempotencyKey string         `json:"idempotency_key" jsonschema:"Stable unique key. Reuse this exact key when retrying the same action"`
}

var descriptions = map[string]string{
	"read_file": "Read a repository file. Args: path, ref.", "get_pr": "Read a pull request. Args: number.", "create_branch": "Create an agent branch. Args: branch, sha (full commit SHA).",
	"put_file":  "Create/update one file on an agent branch. Args: path, branch, content (base64), message, optional sha (existing blob SHA). Workflow/action files are forbidden.",
	"create_pr": "Open a PR from an agent branch. Args: title, head, base, optional body and draft.", "merge_pr": "Request a merge with mandatory operator approval. Args: number, sha (exact full head SHA), optional merge_method. Poll circuit_action_status; do not submit another key while pending or uncertain.",
	"create_issue": "Create an issue. Args: title, optional body.", "update_issue": "Update an issue. Args: number and at least one of title, body, state.",
}

func toolResult(a *Action) *mcp.CallToolResult {
	data, _ := json.Marshal(a)
	return &mcp.CallToolResult{IsError: a.State == "denied" || a.State == "rejected" || a.State == "failed" || a.State == "expired", Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}
func (h *HTTPHandler) mcpServer(agent Agent) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "circuit-github", Version: "0.1.0"}, nil)
	for _, operation := range agent.Actions {
		op := operation
		mcp.AddTool(server, &mcp.Tool{Name: "github_" + op, Description: descriptions[op]}, func(ctx context.Context, req *mcp.CallToolRequest, in toolInput) (*mcp.CallToolResult, any, error) {
			a, err := h.service.Submit(ctx, agent.ID, in.IdempotencyKey, Request{Operation: op, Repository: in.Repository, Args: in.Args})
			if err != nil {
				return nil, nil, err
			}
			return toolResult(a), nil, nil
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "circuit_action_status", Description: "Poll a submitted action by ID. Pending means await operator approval. Uncertain means stop and request reconciliation."}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, any, error) {
		a, err := h.service.store.Get(in.ID)
		if err != nil || a.AgentID != agent.ID {
			return nil, nil, fmt.Errorf("action not found")
		}
		return toolResult(a), nil, nil
	})
	return server
}
