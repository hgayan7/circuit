package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Tokens struct {
	Admin         string
	Agents        map[string]string
	Operators     map[string]string
	WebhookSecret string
}
type principal struct {
	Agent    string
	Admin    bool
	Operator string
	Role     string
}
type identityKey struct{}
type HTTPHandler struct {
	service       *Service
	adminHash     [32]byte
	legacyAdmin   bool
	operators     map[string]operatorCredential
	agents        map[string][32]byte
	mcp           http.Handler
	mux           *http.ServeMux
	webhookPath   string
	webhookSecret string
	draining      atomic.Bool
	requests      atomic.Uint64
	errors        atomic.Uint64
	tlsExpiry     atomic.Int64
	logger        *slog.Logger
	slots         chan struct{}
}

type operatorCredential struct {
	hash [32]byte
	role string
}

func NewHTTPHandler(s *Service, tokens Tokens) (*HTTPHandler, error) {
	if len(s.cfg.Operators) == 0 && len(tokens.Admin) < 32 {
		return nil, fmt.Errorf("admin token must contain at least 32 characters")
	}
	h := &HTTPHandler{
		service:       s,
		adminHash:     sha256.Sum256([]byte(tokens.Admin)),
		agents:        map[string][32]byte{},
		operators:     map[string]operatorCredential{},
		legacyAdmin:   len(s.cfg.Operators) == 0,
		slots:         make(chan struct{}, 64),
		webhookSecret: tokens.WebhookSecret,
	}
	webhookPath := "/webhooks/github"
	if s.cfg.Webhook != nil && s.cfg.Webhook.Path != "" {
		webhookPath = s.cfg.Webhook.Path
	}
	h.webhookPath = webhookPath
	used := map[[32]byte]bool{}
	if h.legacyAdmin {
		used[h.adminHash] = true
	}
	for _, operator := range s.cfg.Operators {
		token := tokens.Operators[operator.ID]
		hash := sha256.Sum256([]byte(token))
		if len(token) < 32 || used[hash] {
			return nil, fmt.Errorf("operator %s needs a distinct token of at least 32 characters", operator.ID)
		}
		used[hash] = true
		h.operators[operator.ID] = operatorCredential{hash: hash, role: operator.Role}
	}
	for _, a := range s.cfg.Agents {
		token := tokens.Agents[a.ID]
		hash := sha256.Sum256([]byte(token))
		if len(token) < 32 || used[hash] {
			return nil, fmt.Errorf("agent %s needs a distinct token of at least 32 characters", a.ID)
		}
		used[hash] = true
		h.agents[a.ID] = hash
	}
	for _, plugin := range s.cfg.CustomTools {
		if plugin.Protocol != PluginProtocol {
			continue
		}
		secret, err := credential(plugin.TokenEnv, plugin.TokenFile)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256([]byte(secret))
		if len(secret) < 32 || used[hash] {
			return nil, fmt.Errorf("plugin %s needs a distinct gateway-only credential", plugin.ID)
		}
		used[hash] = true
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
	mux.HandleFunc("GET /admin/me", h.me)
	mux.HandleFunc("GET /admin/metrics", h.metrics)
	mux.HandleFunc("GET /admin/backup", h.backup)
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /readyz", h.ready)
	mux.HandleFunc("GET /admin/actions/{id}/events", h.events)
	mux.HandleFunc("POST /admin/actions/{id}/decision", h.decide)
	mux.HandleFunc("POST /admin/actions/{id}/reconcile", h.reconcile)
	mux.HandleFunc("POST "+h.webhookPath, h.webhook)
	mux.Handle("/mcp", h.mcp)
	return h, nil
}
func (h *HTTPHandler) authenticate(r *http.Request) (principal, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return principal{}, false
	}
	hash := sha256.Sum256([]byte(token))
	if h.legacyAdmin && subtle.ConstantTimeCompare(hash[:], h.adminHash[:]) == 1 {
		return principal{Admin: true, Role: "admin"}, true
	}
	for id, operator := range h.operators {
		if subtle.ConstantTimeCompare(hash[:], operator.hash[:]) == 1 {
			return principal{Admin: true, Operator: id, Role: operator.role}, true
		}
	}
	for id, expected := range h.agents {
		if subtle.ConstantTimeCompare(hash[:], expected[:]) == 1 {
			return principal{Agent: id}, true
		}
	}
	return principal{}, false
}
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.observe(w, r)
}
func (h *HTTPHandler) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	if h.draining.Load() && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
		writeJSON(w, 503, map[string]string{"error": "Gateway is draining"})
		return
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/" || r.URL.Path == "/ui.js" || (h.webhookPath != "" && r.URL.Path == h.webhookPath) {
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
	if p.Admin && !operatorRouteAllowed(p.Role, r.Method, r.URL.Path) {
		writeJSON(w, 403, map[string]string{"error": "Operator role cannot perform this operation"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	authenticated := r.WithContext(context.WithValue(r.Context(), identityKey{}, p))
	h.mux.ServeHTTP(w, authenticated)
	r.Pattern = authenticated.Pattern
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
		status := 409
		if errors.Is(err, ErrStorageUnavailable) || errors.Is(err, ErrRestorePending) {
			status = 503
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
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
	p := r.Context().Value(identityKey{}).(principal)
	var a *Action
	var err error
	if p.Operator == "" {
		a, err = h.service.Decide(r.Context(), r.PathValue("id"), req.Digest, req.Decision)
	} else {
		a, err = h.service.DecideAs(r.Context(), r.PathValue("id"), req.Digest, req.Decision, p.Operator)
	}
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
	p := r.Context().Value(identityKey{}).(principal)
	var a *Action
	var err error
	if p.Operator == "" {
		a, err = h.service.Reconcile(r.PathValue("id"), req.Digest, req.State, req.Note)
	} else {
		a, err = h.service.ReconcileAs(r.PathValue("id"), req.Digest, req.State, req.Note, p.Operator)
	}
	respondAction(w, a, err)
}
func (h *HTTPHandler) webhook(w http.ResponseWriter, r *http.Request) {
	if h.webhookSecret == "" {
		writeJSON(w, 503, map[string]string{"error": "Webhook secret is not configured on the gateway"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "Payload exceeds 1 MiB or could not be read"})
		return
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !VerifyWebhookSignature(h.webhookSecret, sigHeader, body) {
		writeJSON(w, 401, map[string]string{"error": "Invalid or missing X-Hub-Signature-256"})
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		writeJSON(w, 200, map[string]string{"message": "pong"})
		return
	}
	result, err := h.service.ReconcileWebhook(r.Context(), event, body)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, result)
}

type toolInput struct {
	Repository     string         `json:"repository,omitempty" jsonschema:"Allowed owner/repository name (for GitHub actions)"`
	Workspace      string         `json:"workspace,omitempty" jsonschema:"Allowed workspace ID (for shell/file actions)"`
	Database       string         `json:"database,omitempty" jsonschema:"Allowed database target ID (for SQL actions)"`
	Environment    string         `json:"environment,omitempty" jsonschema:"Allowed cloud deployment environment ID (for cloud actions)"`
	Channel        string         `json:"channel,omitempty" jsonschema:"Allowed communication target ID (for communication actions)"`
	Account        string         `json:"account,omitempty" jsonschema:"Allowed payment account ID (for payment actions)"`
	CustomTool     string         `json:"custom_tool,omitempty" jsonschema:"Allowed custom tool ID"`
	Args           map[string]any `json:"args" jsonschema:"Operation-specific arguments"`
	IdempotencyKey string         `json:"idempotency_key" jsonschema:"Stable unique key. Reuse this exact key when retrying the same action"`
}

var descriptions = map[string]string{
	"read_file":             "Read a repository or workspace file. Args: path, ref (for GitHub).",
	"get_pr":                "Read a pull request. Args: number.",
	"create_branch":         "Create an agent branch. Args: branch, sha (full commit SHA).",
	"put_file":              "Create/update one file on an agent branch. Args: path, branch, content (base64), message, optional sha (existing blob SHA). Workflow/action files are forbidden.",
	"create_pr":             "Open a PR from an agent branch. Args: title, head, base, optional body and draft.",
	"merge_pr":              "Request a merge with mandatory operator approval. Args: number, sha (exact full head SHA), optional merge_method. Poll circuit_action_status; do not submit another key while pending or uncertain.",
	"create_issue":          "Create an issue. Args: title, optional body.",
	"update_issue":          "Update an issue. Args: number and at least one of title, body, state.",
	"exec_cmd":              "Execute a shell command with mandatory operator approval. Requires an external OS sandbox; workspace cwd is not isolation. Args: command, optional cwd (relative), optional timeout_sec.",
	"write_file":            "Create or write a file in the workspace. Overwriting an existing file requires operator review. Args: path, content, optional encoding (base64 or text), optional overwrite (bool).",
	"delete_file":           "Delete a file or directory in the workspace with mandatory operator review. Args: path, optional recursive (bool).",
	"list_dir":              "List entries in a workspace directory. Args: path (relative).",
	"query_sql":             "Run a read-only SQL query (SELECT) against a governed database target. Args: query, optional max_rows, optional timeout_sec.",
	"exec_sql":              "Execute a SQL mutation (INSERT, UPDATE, DELETE, DDL). Destructive statements require operator approval. Args: query, optional timeout_sec, optional max_affected_rows.",
	"list_tables":           "List accessible tables in the database target. Args: optional schema.",
	"describe_table":        "Get schema and column metadata for a table. Args: table, optional schema.",
	"deploy_service":        "Deploy or update a service to an environment. Production changes require operator approval. Args: service, image, optional version.",
	"rollback_deployment":   "Rollback a service to its prior deployment revision with mandatory operator approval. Args: service.",
	"restart_service":       "Restart service containers/pods in an environment. Production restarts require operator approval. Args: service.",
	"get_deployment_status": "Get deployment revision and health status for a service. Args: service.",
	"scale_service":         "Scale service replica count within permitted min/max bounds. Scaling to 0 requires operator review. Args: service, replicas.",
	"send_message":          "Send a message to a team chat or webhook channel. Broadcast mentions (@channel/@here/@everyone) require operator approval. Args: channel, message.",
	"send_email":            "Send an email to specified recipients. External recipient domains require operator review if configured. Args: to, subject, body.",
	"create_ticket":         "Create a new issue/ticket in a tracking system. Args: title, optional description, optional project.",
	"update_ticket":         "Update a ticket status or append comments. Args: key, optional status, optional comment.",
	"publish_document":      "Publish or broadcast a document with mandatory operator approval. Args: title, content.",
	"transfer_funds":        "Transfer funds to an external destination or account. Amounts above threshold require operator approval. Args: amount, destination, optional currency, optional reason.",
	"create_charge":         "Create a customer payment charge. Args: amount, customer_id, optional currency, optional description.",
	"issue_refund":          "Refund a prior payment transaction or charge with operator review. Args: charge_id, optional amount, optional reason.",
	"get_balance":           "Query the current financial balance and ledger summary. Args: optional currency.",
	"call_custom_tool":      "Execute an action or request against an integrated custom tool or internal API. Args: tool, payload, optional operation, optional method, optional endpoint.",
}

func toolResult(a *Action) *mcp.CallToolResult {
	data, _ := json.Marshal(a)
	return &mcp.CallToolResult{IsError: a.State == "denied" || a.State == "rejected" || a.State == "failed" || a.State == "expired", Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}
func (h *HTTPHandler) mcpServer(agent Agent) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "circuit-actions", Version: "0.2.0"}, nil)
	for _, operation := range agent.Actions {
		op := operation
		names := []string{"github_" + op}
		switch op {
		case "exec_cmd":
			names = []string{"shell_exec_cmd"}
		case "write_file":
			names = []string{"file_write"}
		case "delete_file":
			names = []string{"file_delete"}
		case "list_dir":
			names = []string{"file_list_dir"}
		case "query_sql":
			names = []string{"db_query"}
		case "exec_sql":
			names = []string{"db_exec"}
		case "list_tables":
			names = []string{"db_list_tables"}
		case "describe_table":
			names = []string{"db_describe_table"}
		case "deploy_service":
			names = []string{"cloud_deploy"}
		case "rollback_deployment":
			names = []string{"cloud_rollback"}
		case "restart_service":
			names = []string{"cloud_restart"}
		case "get_deployment_status":
			names = []string{"cloud_status"}
		case "scale_service":
			names = []string{"cloud_scale"}
		case "send_message":
			names = []string{"comm_send_message"}
		case "send_email":
			names = []string{"comm_send_email"}
		case "create_ticket":
			names = []string{"comm_create_ticket"}
		case "update_ticket":
			names = []string{"comm_update_ticket"}
		case "publish_document":
			names = []string{"comm_publish_document"}
		case "transfer_funds":
			names = []string{"payment_transfer"}
		case "create_charge":
			names = []string{"payment_charge"}
		case "issue_refund":
			names = []string{"payment_refund"}
		case "get_balance":
			names = []string{"payment_balance"}
		case "call_custom_tool":
			names = []string{"custom_call"}
		case "read_file":
			if len(agent.Workspaces) > 0 && len(agent.Repositories) == 0 {
				names = []string{"file_read"}
			} else if len(agent.Workspaces) > 0 && len(agent.Repositories) > 0 {
				names = []string{"github_read_file", "file_read"}
			}
		default:
			if h.service.cfg.isCustomOperation(op) {
				names = []string{"custom_" + op}
			}
		}
		for _, name := range names {
			tName := name
			desc := descriptions[op]
			if desc == "" {
				desc = fmt.Sprintf("Execute custom action %s on configured custom tool target.", op)
			}
			mcp.AddTool(server, &mcp.Tool{Name: tName, Description: desc}, func(ctx context.Context, req *mcp.CallToolRequest, in toolInput) (*mcp.CallToolResult, any, error) {
				reqPayload := Request{Operation: op, Repository: in.Repository, Workspace: in.Workspace, Database: in.Database, Environment: in.Environment, Channel: in.Channel, Account: in.Account, CustomTool: in.CustomTool, Args: in.Args}
				if tName == "file_read" && reqPayload.Workspace == "" && len(agent.Workspaces) > 0 {
					reqPayload.Workspace = agent.Workspaces[0]
				}
				if isDatabaseOperation(reqPayload) && reqPayload.Database == "" && len(agent.Databases) > 0 {
					reqPayload.Database = agent.Databases[0]
				}
				if isCloudOperation(reqPayload) && reqPayload.Environment == "" && len(agent.Environments) > 0 {
					reqPayload.Environment = agent.Environments[0]
				}
				if isCommOperation(reqPayload) && reqPayload.Channel == "" && len(agent.Channels) > 0 {
					reqPayload.Channel = agent.Channels[0]
				}
				if isPaymentOperation(reqPayload) && reqPayload.Account == "" && len(agent.Accounts) > 0 {
					reqPayload.Account = agent.Accounts[0]
				}
				if (isCustomOperation(reqPayload) || h.service.cfg.isCustomOperation(reqPayload.Operation)) && reqPayload.CustomTool == "" && len(agent.CustomTools) == 1 {
					reqPayload.CustomTool = agent.CustomTools[0]
				}
				a, err := h.service.Submit(ctx, agent.ID, in.IdempotencyKey, reqPayload)
				if err != nil {
					return nil, nil, err
				}
				return toolResult(a), nil, nil
			})
		}
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
