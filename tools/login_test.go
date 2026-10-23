package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
)

func memberServer(t *testing.T) *appie.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":{"member":{"name":{"first":"Jan","last":"Jansen"}}}}`)
	}))
	t.Cleanup(srv.Close)
	return appie.New(appie.WithBaseURL(srv.URL), appie.WithTokens("a", "r"))
}

func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	return r.Content[0].(mcp.TextContent).Text
}

func TestLoginWithCallbackURL(t *testing.T) {
	var gotInput string
	c := memberServer(t)
	deps := Deps{
		IsAuthenticated: func() bool { return false },
		ExchangeCode:    func(_ context.Context, in string) error { gotInput = in; return nil },
		ReloadClient:    func() (*appie.Client, error) { return c, nil },
	}
	res, err := handleLogin(context.Background(), deps, loginArgs{CallbackURL: "appie://login-exit?code=abc12345"})
	if err != nil || res.IsError {
		t.Fatalf("unexpected failure: %v %v", err, res)
	}
	if gotInput != "appie://login-exit?code=abc12345" {
		t.Errorf("exchange got %q", gotInput)
	}
	if !strings.Contains(text(t, res), "Connected as Jan Jansen") {
		t.Errorf("result = %q", text(t, res))
	}
}

func TestLoginWithBadCallbackURLReportsErrorWithoutEchoingInput(t *testing.T) {
	deps := Deps{
		IsAuthenticated: func() bool { return false },
		ExchangeCode: func(context.Context, string) error {
			return errors.New("could not find a login code in the pasted text")
		},
	}
	res, _ := handleLogin(context.Background(), deps, loginArgs{CallbackURL: "secret-looking-input"})
	if !res.IsError || strings.Contains(text(t, res), "secret-looking-input") {
		t.Errorf("want error without echoed input, got %v %q", res.IsError, text(t, res))
	}
}

func TestLoginManualReturnsRealURLAndStartsNoProxy(t *testing.T) {
	deps := Deps{
		IsAuthenticated: func() bool { return false },
		DirectLoginURL:  "https://login.ah.nl/login?client_id=x",
		StartOAuthFlow:  func(context.Context) (string, <-chan error, error) { t.Fatal("proxy started"); return "", nil, nil },
	}
	res, _ := handleLogin(context.Background(), deps, loginArgs{Manual: true})
	out := text(t, res)
	if !strings.Contains(out, "https://login.ah.nl/login?client_id=x") || !strings.Contains(out, "callback_url") {
		t.Errorf("result = %q", out)
	}
}

func TestLoginCallbackURLIgnoredWhenAlreadyLoggedIn(t *testing.T) {
	c := memberServer(t)
	deps := Deps{
		IsAuthenticated: func() bool { return true },
		GetClient:       func() (*appie.Client, error) { return c, nil },
		ExchangeCode:    func(context.Context, string) error { t.Fatal("exchanged while logged in"); return nil },
	}
	res, _ := handleLogin(context.Background(), deps, loginArgs{CallbackURL: "abc12345"})
	if !strings.Contains(text(t, res), "Already connected") {
		t.Errorf("result = %q", text(t, res))
	}
}
