package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Request struct {
	Operation   string         `json:"operation"`
	Repository  string         `json:"repository,omitempty"`
	Workspace   string         `json:"workspace,omitempty"`
	Database    string         `json:"database,omitempty"`
	Environment string         `json:"environment,omitempty"`
	Args        map[string]any `json:"args"`
}
type Outcome struct {
	Status    int             `json:"http_status"`
	Body      json.RawMessage `json:"body,omitempty"`
	Error     string          `json:"error,omitempty"`
	Uncertain bool            `json:"uncertain,omitempty"`
	Warning   string          `json:"warning,omitempty"`
}
type Executor interface {
	Execute(context.Context, Request) Outcome
}
type GitHub struct {
	provider TokenProvider
	base     string
	client   *http.Client
}

func NewGitHub(token string) *GitHub {
	return NewGitHubWithProvider(NewStaticTokenProvider(token))
}

func NewGitHubWithProvider(provider TokenProvider) *GitHub {
	return &GitHub{
		provider: provider,
		base:     "https://api.github.com",
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

var commitSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

func validRef(ref string) bool {
	return ref != "" && len(ref) < 200 && !strings.ContainsAny(ref, " ~^:?*[\\\x00\r\n") && !strings.Contains(ref, "..") && !strings.Contains(ref, "@{") && !strings.HasPrefix(ref, "/") && !strings.HasSuffix(ref, "/") && !strings.HasSuffix(ref, ".") && !strings.HasSuffix(ref, ".lock") && !strings.Contains(ref, "//")
}
func text(args map[string]any, key string) string { s, _ := args[key].(string); return s }
func number(args map[string]any, key string) int {
	n, ok := args[key].(float64)
	if !ok || n <= 0 || n > 2147483647 || n != float64(int(n)) {
		return 0
	}
	return int(n)
}
func forbiddenPath(p string) bool {
	p = strings.ToLower(p)
	return p == ".github/workflows" || strings.HasPrefix(p, ".github/workflows/") || p == ".github/actions" || strings.HasPrefix(p, ".github/actions/") || p == ".git" || strings.HasPrefix(p, ".git/")
}
func validateRequest(req *Request) error {
	if !operations[req.Operation] {
		return fmt.Errorf("unsupported operation %q", req.Operation)
	}
	if isWorkspaceOperation(*req) {
		if req.Workspace == "" {
			req.Workspace = "default"
		}
		if !identifier.MatchString(req.Workspace) {
			return fmt.Errorf("invalid workspace identifier %q", req.Workspace)
		}
		wsFields := map[string][]string{
			"exec_cmd":    {"command", "cwd", "timeout_sec"},
			"read_file":   {"path"},
			"write_file":  {"path", "content", "encoding", "overwrite"},
			"delete_file": {"path", "recursive"},
			"list_dir":    {"path"},
		}
		for k := range req.Args {
			if !member(wsFields[req.Operation], k) {
				return fmt.Errorf("unexpected argument %q for %s", k, req.Operation)
			}
		}
		if req.Operation == "exec_cmd" {
			if text(req.Args, "command") == "" {
				return fmt.Errorf("command is required")
			}
		} else {
			if req.Operation != "list_dir" && text(req.Args, "path") == "" {
				return fmt.Errorf("path is required")
			}
		}
		if p := text(req.Args, "path"); p != "" {
			if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || strings.Contains(p, "\x00") {
				return fmt.Errorf("path must be a relative path without null bytes")
			}
		}
		return nil
	}

	if isDatabaseOperation(*req) {
		if req.Database == "" {
			req.Database = "default"
		}
		if !identifier.MatchString(req.Database) {
			return fmt.Errorf("invalid database identifier %q", req.Database)
		}
		dbFields := map[string][]string{
			"query_sql":      {"query", "max_rows", "timeout_sec"},
			"exec_sql":       {"query", "timeout_sec", "max_affected_rows"},
			"list_tables":    {"schema"},
			"describe_table": {"table", "schema"},
		}
		for k := range req.Args {
			if !member(dbFields[req.Operation], k) {
				return fmt.Errorf("unexpected argument %q for %s", k, req.Operation)
			}
		}
		if req.Operation == "query_sql" || req.Operation == "exec_sql" {
			if text(req.Args, "query") == "" {
				return fmt.Errorf("query is required")
			}
		} else if req.Operation == "describe_table" {
			if text(req.Args, "table") == "" {
				return fmt.Errorf("table is required")
			}
		}
		return nil
	}

	if isCloudOperation(*req) {
		if req.Environment == "" {
			req.Environment = "default"
		}
		if !identifier.MatchString(req.Environment) {
			return fmt.Errorf("invalid environment identifier %q", req.Environment)
		}
		cloudFields := map[string][]string{
			"deploy_service":        {"service", "image", "version"},
			"rollback_deployment":   {"service"},
			"restart_service":       {"service"},
			"get_deployment_status": {"service"},
			"scale_service":         {"service", "replicas"},
		}
		for k := range req.Args {
			if !member(cloudFields[req.Operation], k) {
				return fmt.Errorf("unexpected argument %q for %s", k, req.Operation)
			}
		}
		if text(req.Args, "service") == "" {
			return fmt.Errorf("service is required")
		}
		if req.Operation == "deploy_service" && text(req.Args, "image") == "" {
			return fmt.Errorf("image is required")
		}
		if req.Operation == "scale_service" {
			if _, ok := req.Args["replicas"]; !ok {
				return fmt.Errorf("replicas is required")
			}
		}
		return nil
	}

	req.Repository = strings.ToLower(req.Repository)
	if !repoPattern.MatchString(req.Repository) {
		return fmt.Errorf("unsupported operation or invalid repository")
	}
	fields := map[string][]string{
		"read_file": {"path", "ref"}, "get_pr": {"number"}, "create_branch": {"branch", "sha"}, "put_file": {"path", "branch", "sha", "content", "message"}, "create_pr": {"title", "body", "head", "base", "draft"}, "merge_pr": {"number", "sha", "merge_method"}, "create_issue": {"title", "body"}, "update_issue": {"number", "title", "body", "state"},
	}
	for k := range req.Args {
		if !member(fields[req.Operation], k) {
			return fmt.Errorf("unexpected argument %q", k)
		}
	}
	for _, k := range fields[req.Operation] {
		if v, ok := req.Args[k]; ok {
			if k == "number" {
				if number(req.Args, k) == 0 {
					return fmt.Errorf("number must be a positive integer")
				}
			} else if k == "draft" {
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("draft must be boolean")
				}
			} else if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be text", k)
			}
		}
	}
	required := map[string][]string{"read_file": {"path", "ref"}, "get_pr": {"number"}, "create_branch": {"branch", "sha"}, "put_file": {"path", "branch", "content", "message"}, "create_pr": {"title", "head", "base"}, "merge_pr": {"number", "sha"}, "create_issue": {"title"}, "update_issue": {"number"}}
	for _, k := range required[req.Operation] {
		if k == "number" {
			if number(req.Args, k) == 0 {
				return fmt.Errorf("number is required")
			}
		} else if text(req.Args, k) == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	for _, key := range []string{"branch", "head", "base", "ref"} {
		if val := text(req.Args, key); val != "" && !validRef(val) {
			return fmt.Errorf("invalid %s ref", key)
		}
	}
	if sha := text(req.Args, "sha"); sha != "" && !commitSHA.MatchString(sha) {
		return fmt.Errorf("sha must be a full 40-character commit/blob SHA")
	}
	if p := text(req.Args, "path"); p != "" {
		if path.Clean(p) != p || strings.HasPrefix(p, "/") || p == "." || strings.ContainsAny(p, "%\\\x00\r\n") || strings.HasPrefix(p, "../") {
			return fmt.Errorf("path must be a canonical relative repository path")
		}
	}
	if req.Operation == "put_file" {
		decoded, err := base64.StdEncoding.DecodeString(text(req.Args, "content"))
		if err != nil || len(decoded) > 256<<10 {
			return fmt.Errorf("content must be base64 and at most 256 KiB")
		}
	}
	if req.Operation == "update_issue" {
		if len(req.Args) < 2 {
			return fmt.Errorf("update_issue needs a changed field")
		}
		if state := text(req.Args, "state"); state != "" && state != "open" && state != "closed" {
			return fmt.Errorf("invalid issue state")
		}
	}
	if method := text(req.Args, "merge_method"); method != "" && !member([]string{"merge", "squash", "rebase"}, method) {
		return fmt.Errorf("invalid merge method")
	}
	return nil
}
func validateScope(agent *Agent, req Request) error {
	if isCloudOperation(req) {
		envID := req.Environment
		if envID == "" {
			envID = "default"
		}
		if !member(agent.Environments, envID) {
			return fmt.Errorf("Access to cloud environment %q is not permitted for this agent", envID)
		}
		return nil
	}
	if isDatabaseOperation(req) {
		dbID := req.Database
		if dbID == "" {
			dbID = "default"
		}
		if !member(agent.Databases, dbID) {
			return fmt.Errorf("Access to database %q is not permitted for this agent", dbID)
		}
		return nil
	}
	if isWorkspaceOperation(req) {
		wsID := req.Workspace
		if wsID == "" {
			wsID = "default"
		}
		if !member(agent.Workspaces, wsID) {
			return fmt.Errorf("Access to workspace %q is not permitted for this agent", wsID)
		}
		return nil
	}
	if req.Operation == "put_file" && forbiddenPath(text(req.Args, "path")) {
		return fmt.Errorf("Changes to workflow/action configuration are forbidden")
	}
	if req.Operation == "create_branch" || req.Operation == "put_file" {
		if !strings.HasPrefix(text(req.Args, "branch"), agent.BranchPrefix) {
			return fmt.Errorf("Writes are restricted to the agent branch prefix %s", agent.BranchPrefix)
		}
	}
	if req.Operation == "create_pr" && !strings.HasPrefix(text(req.Args, "head"), agent.BranchPrefix) {
		return fmt.Errorf("PR head must use the agent branch prefix")
	}
	return nil
}
func escapedPath(p string) string {
	parts := strings.Split(p, "/")
	for j := range parts {
		parts[j] = url.PathEscape(parts[j])
	}
	return strings.Join(parts, "/")
}
func (g *GitHub) call(ctx context.Context, token, method, endpoint string, payload any) Outcome {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return Outcome{Error: "Cannot encode GitHub request"}
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+endpoint, body)
	if err != nil {
		return Outcome{Error: "Invalid GitHub request"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	resp, err := g.client.Do(req)
	if err != nil {
		return Outcome{Error: "GitHub transport failed; outcome requires reconciliation", Uncertain: method != "GET"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return Outcome{Status: resp.StatusCode, Error: "GitHub response could not be read", Uncertain: method != "GET"}
	}
	// Never expose the credential even if a misconfigured upstream echoes it.
	if token != "" {
		data = []byte(strings.ReplaceAll(string(data), token, "[redacted]"))
	}
	if !json.Valid(data) {
		return Outcome{Status: resp.StatusCode, Error: "GitHub returned an invalid JSON response", Uncertain: method != "GET"}
	}
	result := Outcome{Status: resp.StatusCode, Body: data}
	if resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("GitHub returned HTTP %d", resp.StatusCode)
		result.Uncertain = resp.StatusCode >= 500 && method != "GET"
	}
	return result
}
func (g *GitHub) Execute(ctx context.Context, r Request) Outcome {
	token, err := g.provider.Token(ctx, r.Repository)
	if err != nil {
		return Outcome{Error: fmt.Sprintf("Authentication failed: %v", err)}
	}
	prefix := "/repos/" + r.Repository
	switch r.Operation {
	case "read_file":
		return g.call(ctx, token, "GET", prefix+"/contents/"+escapedPath(text(r.Args, "path"))+"?ref="+url.QueryEscape(text(r.Args, "ref")), nil)
	case "get_pr":
		return g.call(ctx, token, "GET", fmt.Sprintf("%s/pulls/%d", prefix, number(r.Args, "number")), nil)
	case "create_branch":
		return g.call(ctx, token, "POST", prefix+"/git/refs", map[string]any{"ref": "refs/heads/" + text(r.Args, "branch"), "sha": text(r.Args, "sha")})
	case "put_file":
		payload := map[string]any{}
		for k, v := range r.Args {
			if k != "path" {
				payload[k] = v
			}
		}
		return g.call(ctx, token, "PUT", prefix+"/contents/"+escapedPath(text(r.Args, "path")), payload)
	case "create_pr":
		return g.call(ctx, token, "POST", prefix+"/pulls", r.Args)
	case "create_issue":
		return g.call(ctx, token, "POST", prefix+"/issues", r.Args)
	case "update_issue":
		payload := map[string]any{}
		for k, v := range r.Args {
			if k != "number" {
				payload[k] = v
			}
		}
		return g.call(ctx, token, "PATCH", fmt.Sprintf("%s/issues/%d", prefix, number(r.Args, "number")), payload)
	case "merge_pr":
		current := g.call(ctx, token, "GET", fmt.Sprintf("%s/pulls/%d", prefix, number(r.Args, "number")), nil)
		if current.Error != "" {
			return current
		}
		var pr struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		if json.Unmarshal(current.Body, &pr) != nil || !strings.EqualFold(pr.Head.SHA, text(r.Args, "sha")) {
			return Outcome{Error: "PR head changed; propose a new merge for approval"}
		}
		// Check every changed file, then use GitHub's SHA precondition on the write.
		for page := 1; page <= 10; page++ {
			res := g.call(ctx, token, "GET", fmt.Sprintf("%s/pulls/%d/files?per_page=100&page=%d", prefix, number(r.Args, "number"), page), nil)
			if res.Error != "" {
				return res
			}
			var files []struct {
				Filename         string `json:"filename"`
				PreviousFilename string `json:"previous_filename"`
			}
			if err := json.Unmarshal(res.Body, &files); err != nil {
				return Outcome{Error: "Cannot inspect changed files"}
			}
			for _, f := range files {
				if forbiddenPath(f.Filename) || forbiddenPath(f.PreviousFilename) {
					return Outcome{Error: "Merge includes forbidden workflow/action changes"}
				}
			}
			if len(files) < 100 {
				payload := map[string]any{"sha": text(r.Args, "sha")}
				if method := text(r.Args, "merge_method"); method != "" {
					payload["merge_method"] = method
				}
				// Recheck the head after listing files, then pin it again on the write.
				latest := g.call(ctx, token, "GET", fmt.Sprintf("%s/pulls/%d", prefix, number(r.Args, "number")), nil)
				if latest.Error != "" {
					return latest
				}
				if json.Unmarshal(latest.Body, &pr) != nil || !strings.EqualFold(pr.Head.SHA, text(r.Args, "sha")) {
					return Outcome{Error: "PR head changed during review; propose a new merge"}
				}
				merged := g.call(ctx, token, "PUT", fmt.Sprintf("%s/pulls/%d/merge", prefix, number(r.Args, "number")), payload)
				if merged.Error == "" {
					var result struct {
						Merged bool `json:"merged"`
					}
					if json.Unmarshal(merged.Body, &result) != nil || !result.Merged {
						merged.Error = "GitHub did not confirm the merge"
					}
				}
				return merged
			}
		}
		return Outcome{Error: "PR exceeds 1,000-file inspection limit"}
	}
	return Outcome{Error: "Unsupported GitHub operation"}
}
