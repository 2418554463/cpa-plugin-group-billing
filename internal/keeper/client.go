// Package keeper is a synchronous, cookie-authenticated adapter to Usage Keeper.
// It never owns a worker/ticker, forwards CPA credentials, or persists raw keys.
package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"cpa-key-billing/internal/billing"
)

type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string { return "Keeper: " + e.Code }

type Resolver func(authType int, authIndex string) (string, bool)
type Client struct {
	mu                          sync.Mutex
	base, passwordEnv, instance string
	jar                         http.CookieJar
	timeout                     time.Duration
}

func New(base, passwordEnv string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("keeper_url 必须为无凭据、查询参数和片段的 HTTP(S) 地址")
	}
	if strings.TrimSpace(passwordEnv) == "" {
		return nil, errors.New("Keeper 必须配置 keeper_password_env 环境变量名")
	}
	for _, r := range passwordEnv {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return nil, errors.New("Keeper 环境变量名无效")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.Host = strings.ToLower(u.Host)
	canonical := u.String()
	sum := sha256.Sum256([]byte(canonical))
	jar, _ := cookiejar.New(nil)
	return &Client{base: canonical, passwordEnv: passwordEnv, instance: hex.EncodeToString(sum[:]), jar: jar, timeout: 10 * time.Second}, nil
}
func (c *Client) Instance() string { return c.instance }
func (c *Client) Ready() bool      { return os.Getenv(c.passwordEnv) != "" }

// Every operation closes connections before returning to the embedded runtime.
func (c *Client) session(fn func(context.Context, *http.Client) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: c.jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fn(ctx, client)
}
func (c *Client) request(ctx context.Context, client *http.Client, method, path string, payload any, out any, login bool) error {
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		return &Error{"invalid_request", 502}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CPA-Usage-Keeper-Request", "fetch")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		code := "unavailable"
		if method == http.MethodPatch {
			code = "write_outcome_unknown"
		}
		return &Error{code, 503}
	}
	defer response.Body.Close()
	if response.StatusCode == 401 && login {
		response.Body.Close()
		password := os.Getenv(c.passwordEnv)
		if password == "" {
			return &Error{"password_env_missing", 503}
		}
		if err := c.request(ctx, client, http.MethodPost, "/auth/login", map[string]string{"password": password}, nil, false); err != nil {
			return err
		}
		return c.request(ctx, client, method, path, payload, out, false)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "remote_rejected"
		status := 502
		if response.StatusCode == 401 || response.StatusCode == 403 {
			code = "authentication_failed"
			status = 503
		}
		if method == http.MethodPatch && response.StatusCode >= 500 {
			code = "write_outcome_unknown"
		}
		return &Error{code, status}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil || len(body) > 8*1024*1024 {
		code := "invalid_response"
		if method == http.MethodPatch {
			code = "write_outcome_unknown"
		}
		return &Error{code, 502}
	}
	if out != nil && json.Unmarshal(body, out) != nil {
		return &Error{"invalid_response", 502}
	}
	return nil
}
func validID(id string) bool {
	if len(id) < 1 || len(id) > 19 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0
}
func (c *Client) snapshot(ctx context.Context, client *http.Client, resolve Resolver) ([]billing.SharedLabel, error) {
	var keys struct {
		Items *[]struct {
			ID     string `json:"id"`
			APIKey string `json:"apiKey"`
			Alias  string `json:"keyAlias"`
		} `json:"items"`
	}
	if err := c.request(ctx, client, "GET", "/usage/api-keys/settings", nil, &keys, true); err != nil {
		return nil, err
	}
	if keys.Items == nil {
		return nil, &Error{"incompatible_api_keys_response", 502}
	}
	result := []billing.SharedLabel{}
	seen := map[string]bool{}
	now := time.Now().UTC()
	add := func(kind, subject, id, value, note string) error {
		key := kind + "/" + subject
		if seen[key] {
			return &Error{"ambiguous_identity", 409}
		}
		seen[key] = true
		numericID, _ := strconv.ParseInt(id, 10, 64)
		var sourceNote *string
		if kind == "upstream_identity" {
			sourceNote = &note
		}
		result = append(result, billing.SharedLabel{InstanceID: c.instance, Kind: kind, SubjectID: subject, ExternalID: numericID, Value: value, SourceNote: sourceNote, FetchedAt: now})
		return nil
	}
	for _, k := range *keys.Items {
		scope := billing.CallerScope(k.APIKey)
		k.APIKey = ""
		if scope == "" || !validID(k.ID) {
			continue
		}
		if err := add("downstream_key", scope, k.ID, k.Alias, ""); err != nil {
			return nil, err
		}
	}
	var identities struct {
		Items *[]struct {
			ID       string  `json:"id"`
			AuthType int     `json:"auth_type"`
			Identity string  `json:"identity"`
			Alias    *string `json:"alias"`
			Note     *string `json:"note"`
			Deleted  bool    `json:"is_deleted"`
		} `json:"identities"`
	}
	if err := c.request(ctx, client, "GET", "/usage/identities", nil, &identities, true); err != nil {
		return nil, err
	}
	if identities.Items == nil {
		return nil, &Error{"incompatible_identities_response", 502}
	}
	for _, v := range *identities.Items {
		if v.Deleted || !validID(v.ID) || resolve == nil {
			continue
		}
		ref, ok := resolve(v.AuthType, v.Identity)
		if !ok {
			continue
		}
		alias, note := "", ""
		if v.Alias != nil {
			alias = *v.Alias
		}
		if v.Note != nil {
			note = *v.Note
		}
		if err := add("upstream_identity", ref, v.ID, alias, note); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (c *Client) Snapshot(resolve Resolver) (labels []billing.SharedLabel, err error) {
	err = c.session(func(ctx context.Context, httpClient *http.Client) error {
		labels, err = c.snapshot(ctx, httpClient, resolve)
		return err
	})
	return
}

// A fresh exact-identity lookup prevents stale numeric IDs from editing a different
// account. Keeper has no CAS: expected-value checking is best effort, not atomic.
func (c *Client) Update(kind, subject, value string, expected *string, resolve Resolver) (labels []billing.SharedLabel, err error) {
	value = strings.TrimSpace(value)
	max := 128
	if kind == "upstream_identity" {
		max = 50
	} else if kind != "downstream_key" {
		return nil, &Error{"invalid_subject_kind", 400}
	}
	if len([]rune(value)) > max {
		return nil, &Error{"label_too_long", 400}
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return nil, &Error{"invalid_label", 400}
		}
	}
	err = c.session(func(ctx context.Context, httpClient *http.Client) error {
		current, e := c.snapshot(ctx, httpClient, resolve)
		if e != nil {
			return e
		}
		var selected *billing.SharedLabel
		for i := range current {
			if current[i].Kind == kind && current[i].SubjectID == subject {
				selected = &current[i]
			}
		}
		if selected == nil {
			return &Error{"mapping_not_found", 409}
		}
		if expected != nil && selected.Value != *expected {
			return &Error{"label_conflict", 409}
		}
		path, field := "/usage/api-keys/", "keyAlias"
		if kind == "upstream_identity" {
			path, field = "/usage/identities/", "alias"
		}
		if e := c.request(ctx, httpClient, "PATCH", path+strconv.FormatInt(selected.ExternalID, 10), map[string]string{field: value}, nil, true); e != nil {
			return e
		}
		labels, e = c.snapshot(ctx, httpClient, resolve)
		if e != nil {
			return &Error{"write_outcome_unknown", 503}
		}
		for _, l := range labels {
			if l.Kind == kind && l.SubjectID == subject && l.Value == value {
				return nil
			}
		}
		return &Error{"write_outcome_unknown", 503}
	})
	return
}
func PublicError(err error) (int, string) {
	var value *Error
	if errors.As(err, &value) {
		return value.Status, value.Code
	}
	return 503, fmt.Sprint("unavailable")
}
