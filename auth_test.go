package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLoginCode(t *testing.T) {
	good := map[string]string{
		"appie://login-exit?code=abc12345-def":                 "abc12345-def",
		"http://localhost:9876/callback?code=abc12345&foo=bar": "abc12345",
		"  abc12345-6789-def0  \n":                             "abc12345-6789-def0",
		"appie://login-exit?state=x&code=zzzzzzzz":             "zzzzzzzz",
	}
	for in, want := range good {
		got, err := ParseLoginCode(in)
		if err != nil || got != want {
			t.Errorf("ParseLoginCode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "appie://login-exit", "https://login.ah.nl/login?x=1", "not a code!", "short"} {
		if got, err := ParseLoginCode(in); err == nil {
			t.Errorf("ParseLoginCode(%q) = %q, want error", in, got)
		}
	}
}

func TestExchangeCodeSavesTokens(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		io.WriteString(w, `{"access_token":"A","refresh_token":"R","member_id":"7","expires_in":3600}`)
	}))
	defer srv.Close()
	old := ahAPIBase
	ahAPIBase = srv.URL
	defer func() { ahAPIBase = old }()

	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := ExchangeCode(context.Background(), "appie://login-exit?code=abc12345", path); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/mobile-auth/v1/auth/token" || gotBody["code"] != "abc12345" || gotBody["clientId"] != "appie-ios" {
		t.Errorf("unexpected request: %s %v", gotPath, gotBody)
	}
	tf, err := LoadTokens(path)
	if err != nil || tf == nil || tf.AccessToken != "A" || tf.RefreshToken != "R" {
		t.Fatalf("tokens not saved: %+v, %v", tf, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0600 {
		t.Errorf("tokens mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestExchangeCodeRejectsGarbageWithoutRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	old := ahAPIBase
	ahAPIBase = srv.URL
	defer func() { ahAPIBase = old }()

	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := ExchangeCode(context.Background(), "nope!", path); err == nil {
		t.Fatal("want error")
	}
	if called {
		t.Error("API called for unparseable input")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("tokens written for unparseable input")
	}
}

func TestStartRedirectsToRealLogin(t *testing.T) {
	loginURL, _, err := StartOAuthFlow(context.Background(), "http://localhost:19876", 19876, filepath.Join(t.TempDir(), "t.json"), true)
	if err != nil {
		t.Skipf("port unavailable: %v", err)
	}
	if loginURL != "http://localhost:19876/start" {
		t.Errorf("loginURL = %q", loginURL)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://localhost:19876/start")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != DirectLoginURL() {
		t.Errorf("got %d Location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	flowStopMu.Lock()
	stop := flowStop
	flowStopMu.Unlock()
	stop()
}
