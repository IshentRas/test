package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type CoderClient struct {
	BaseURL    string
	OwnerToken string
	HTTP       *http.Client
}

func NewCoderClient(baseURL, ownerToken string) *CoderClient {
	return &CoderClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		OwnerToken: ownerToken,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
	}
}

type coderUser struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type coderUsersResp struct {
	Users []coderUser `json:"users"`
	Count int         `json:"count"`
}

type coderTokenResp struct {
	Key string `json:"key"`
}

type coderWorkspace struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	OwnerName        string          `json:"owner_name"`
	TemplateID       string          `json:"template_id"`
	TemplateName     string          `json:"template_name"`
	LatestBuild      json.RawMessage `json:"latest_build"`
	Outdated         bool            `json:"outdated"`
	OrganizationID   string          `json:"organization_id"`
}

type coderWorkspacesResp struct {
	Workspaces []coderWorkspace `json:"workspaces"`
	Count      int              `json:"count"`
}

type coderTemplate struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	ActiveVersionID string `json:"active_version_id"`
}

type coderHTTPError struct {
	Status  int
	Message string
}

func (e *coderHTTPError) Error() string { return e.Message }

func (c *CoderClient) do(ctx context.Context, method, path string, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Coder-Session-Token", token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		msg := truncate(string(data), 400)
		return &coderHTTPError{
			Status:  res.StatusCode,
			Message: fmt.Sprintf("coder %s %s: %s — %s", method, path, res.Status, msg),
		}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *CoderClient) FindUserByEmail(ctx context.Context, email string) (*coderUser, error) {
	// Search by raw email string (Coder rejects "email:" as a filter key on some versions).
	q := url.Values{}
	q.Set("q", email)
	var resp coderUsersResp
	if err := c.do(ctx, http.MethodGet, "/api/v2/users?"+q.Encode(), c.OwnerToken, nil, &resp); err != nil {
		// Some versions return a bare array
		var arr []coderUser
		if err2 := c.do(ctx, http.MethodGet, "/api/v2/users?"+q.Encode(), c.OwnerToken, nil, &arr); err2 == nil {
			for _, u := range arr {
				if strings.EqualFold(u.Email, email) {
					return &u, nil
				}
			}
			return nil, nil
		}
		return nil, err
	}
	for _, u := range resp.Users {
		if strings.EqualFold(u.Email, email) {
			return &u, nil
		}
	}
	return nil, nil
}

func usernameFromEmail(email string) string {
	local := strings.Split(email, "@")[0]
	local = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		default:
			return '-'
		}
	}, local)
	if local == "" {
		local = "user"
	}
	if len(local) > 28 {
		local = local[:28]
	}
	return local
}

func (c *CoderClient) CreateUser(ctx context.Context, email string) (*coderUser, error) {
	var meFull struct {
		OrganizationIDs []string `json:"organization_ids"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/users/me", c.OwnerToken, nil, &meFull); err != nil {
		return nil, fmt.Errorf("owner me: %w", err)
	}
	if len(meFull.OrganizationIDs) == 0 {
		return nil, fmt.Errorf("owner has no organization_ids")
	}
	body := map[string]any{
		"email":           email,
		"username":        usernameFromEmail(email),
		"password":        "Aa1!" + mustRandomHex(16),
		"organization_id": meFull.OrganizationIDs[0],
		"name":            usernameFromEmail(email),
	}
	var created coderUser
	if err := c.do(ctx, http.MethodPost, "/api/v2/users", c.OwnerToken, body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *CoderClient) FindOrCreateUser(ctx context.Context, email string) (*coderUser, error) {
	u, err := c.FindUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	return c.CreateUser(ctx, email)
}

func (c *CoderClient) CreateUserToken(ctx context.Context, userID, name string) (string, error) {
	// Coder expects lifetime as time.Duration nanoseconds in JSON.
	body := map[string]any{
		"token_name": name + "-" + mustRandomHex(6),
		"lifetime":   int64(7 * 24 * time.Hour), // 7d in ns
	}
	var resp coderTokenResp
	if err := c.do(ctx, http.MethodPost, "/api/v2/users/"+userID+"/keys/tokens", c.OwnerToken, body, &resp); err != nil {
		return "", err
	}
	if resp.Key == "" {
		return "", fmt.Errorf("empty token key from coder")
	}
	return resp.Key, nil
}

// PingToken returns nil if the Coder session token is still valid.
func (c *CoderClient) PingToken(ctx context.Context, userToken string) error {
	return c.do(ctx, http.MethodGet, "/api/v2/users/me", userToken, nil, &coderUser{})
}

func (c *CoderClient) ListWorkspaces(ctx context.Context, userToken, ownerUsername string) ([]coderWorkspace, error) {
	q := url.Values{}
	if ownerUsername != "" {
		q.Set("q", "owner:"+ownerUsername)
	}
	path := "/api/v2/workspaces"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var resp coderWorkspacesResp
	if err := c.do(ctx, http.MethodGet, path, userToken, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Workspaces, nil
}

func (c *CoderClient) GetMe(ctx context.Context, userToken string) (*coderUser, error) {
	var u coderUser
	if err := c.do(ctx, http.MethodGet, "/api/v2/users/me", userToken, nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *CoderClient) ListTemplates(ctx context.Context, userToken string) ([]coderTemplate, error) {
	var resp struct {
		Templates []coderTemplate `json:"templates"`
		Count     int             `json:"count"`
	}
	// Prefer org templates via /templates
	if err := c.do(ctx, http.MethodGet, "/api/v2/templates", userToken, nil, &resp); err != nil {
		// Some versions return a bare array
		var arr []coderTemplate
		if err2 := c.do(ctx, http.MethodGet, "/api/v2/templates", userToken, nil, &arr); err2 != nil {
			return nil, err
		}
		return arr, nil
	}
	return resp.Templates, nil
}

func (c *CoderClient) FindTemplateID(ctx context.Context, userToken, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("template name/id required")
	}
	// UUID-ish
	if len(nameOrID) == 36 && strings.Count(nameOrID, "-") == 4 {
		return nameOrID, nil
	}
	tmpls, err := c.ListTemplates(ctx, userToken)
	if err != nil {
		return "", err
	}
	for _, t := range tmpls {
		if t.ID == nameOrID || t.Name == nameOrID || t.DisplayName == nameOrID {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("template %q not found", nameOrID)
}

func (c *CoderClient) CreateWorkspace(ctx context.Context, userToken, username, name, templateID string) (*coderWorkspace, error) {
	body := map[string]any{
		"name":        name,
		"template_id": templateID,
	}
	var ws coderWorkspace
	path := "/api/v2/users/" + url.PathEscape(username) + "/workspaces"
	if err := c.do(ctx, http.MethodPost, path, userToken, body, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

// DeleteWorkspace starts a delete-transition build for the workspace.
func (c *CoderClient) DeleteWorkspace(ctx context.Context, userToken, workspaceID string) (map[string]any, error) {
	body := map[string]any{"transition": "delete"}
	var build map[string]any
	path := "/api/v2/workspaces/" + url.PathEscape(workspaceID) + "/builds"
	if err := c.do(ctx, http.MethodPost, path, userToken, body, &build); err != nil {
		return nil, err
	}
	return build, nil
}

// WorkspaceRaw fetches the workspace JSON (includes agents/resources for apps/pty).
func (c *CoderClient) WorkspaceRaw(ctx context.Context, userToken, id string) (map[string]any, error) {
	var raw map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/v2/workspaces/"+id, userToken, nil, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}
