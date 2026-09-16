package keeper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestCookieSessionExactIdentityAndTwoWayLabels(t *testing.T) {
	t.Setenv("TEST_KEEPER_PASSWORD", "dummy-keeper-password")
	var mu sync.Mutex
	alias := "original"
	upstream := "upstream note"
	writes, logins := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "" {
			t.Error("CPA authorization must not be forwarded")
		}
		if r.Header.Get("X-CPA-Usage-Keeper-Request") != "fetch" {
			t.Error("missing write protection header")
		}
		if r.URL.Path == "/keeper/api/v1/auth/login" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != "dummy-keeper-password" {
				t.Error("wrong own password")
			}
			logins++
			http.SetCookie(w, &http.Cookie{Name: "cpa_usage_keeper_session", Value: "dummy-session", Path: "/keeper/", HttpOnly: true})
			_, _ = w.Write([]byte(`{}`))
			return
		}
		cookie, err := r.Cookie("cpa_usage_keeper_session")
		if err != nil || cookie.Value != "dummy-session" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/keeper/api/v1/usage/api-keys/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": "21", "apiKey": "dummy-downstream-key", "keyAlias": alias}}})
		case "/keeper/api/v1/usage/identities":
			_ = json.NewEncoder(w).Encode(map[string]any{"identities": []any{map[string]any{"id": "25", "identity": "auth-index-exact", "auth_type": 1, "alias": upstream, "note": "CPA metadata"}, map[string]any{"id": "26", "identity": "unknown-index", "auth_type": 2, "alias": "not matched"}}})
		case "/keeper/api/v1/usage/api-keys/21":
			if r.Method != "PATCH" {
				t.Error("method")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			alias = body["keyAlias"]
			writes++
			_, _ = w.Write([]byte(`{}`))
		case "/keeper/api/v1/usage/identities/25":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			upstream = body["alias"]
			writes++
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, err := New(server.URL+"/keeper/", "TEST_KEEPER_PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	ref := billing.CredentialFingerprint("dummy-auth-id")
	resolve := func(kind int, index string) (string, bool) { return ref, kind == 1 && index == "auth-index-exact" }
	labels, err := c.Snapshot(resolve)
	if err != nil || len(labels) != 2 {
		t.Fatalf("snapshot %v %v", labels, err)
	}
	raw, _ := json.Marshal(labels)
	if strings.Contains(string(raw), "dummy-downstream-key") || strings.Contains(string(raw), "auth-index-exact") || strings.Contains(string(raw), "password") {
		t.Fatal("secret/raw identity in cache")
	}
	scope := billing.CallerScope("dummy-downstream-key")
	expected := "original"
	labels, err = c.Update("downstream_key", scope, "from billing", &expected, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if labels[0].Value != "from billing" {
		t.Fatal(labels)
	}
	mu.Lock()
	alias = "from Keeper"
	mu.Unlock()
	labels, err = c.Snapshot(resolve)
	if err != nil || labels[0].Value != "from Keeper" {
		t.Fatal("reverse sync", err)
	}
	if _, err = c.Update("downstream_key", scope, "lost update", &expected, resolve); err == nil {
		t.Fatal("missing conflict")
	}
	expected = "upstream note"
	if _, err = c.Update("upstream_identity", ref, "", &expected, resolve); err != nil {
		t.Fatal("empty alias", err)
	}
	if _, err = c.Update("downstream_key", billing.CallerScope("different-key"), "wrong", nil, resolve); err == nil {
		t.Fatal("matched absent key")
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 2 || logins != 1 {
		t.Fatalf("writes %d logins %d", writes, logins)
	}
}

func TestKeeperErrorsDoNotLeakAndWritesNotRetried(t *testing.T) {
	for _, status := range []int{403, 500, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Location", "https://example.invalid")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("dummy-secret-in-error"))
			}))
			defer server.Close()
			c, _ := New(server.URL, "DUMMY_ENV")
			_, err := c.Snapshot(nil)
			if err == nil || strings.Contains(err.Error(), "dummy-secret") || requests != 1 {
				t.Fatalf("%v requests %d", err, requests)
			}
		})
	}
	for _, base := range []string{"ftp://host", "https://user:password@host", "https://host/?secret=1", "https://host/#frag", "host"} {
		if _, err := New(base, "DUMMY_ENV"); err == nil {
			t.Fatalf("accepted %s", base)
		}
	}
}
