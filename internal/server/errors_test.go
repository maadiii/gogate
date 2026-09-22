package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hertz "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/pkg/errors"
	"github.com/maadiii/gogate/pkg/hooks"
	"github.com/maadiii/goutils/auth"
)

// The tests in this file drive a real Hertz server with the ErrorHandler
// middleware installed, because that middleware is the only place an error
// response is shaped. Testing the pipeline without it would pass while the
// client saw a bare status and no body at all.

const testTokenSecret = "0123456789abcdef0123456789abcdef"

// hertzAddrWithErrorHandler boots a real Hertz server in front of gw with the
// middleware installed the way main.go installs it — before the route, so that
// it wraps every request. It returns the server's host:port.
func hertzAddrWithErrorHandler(t *testing.T, gw *Gateway, isProd bool) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding listener: %v", err)
	}

	h := hertz.New(hertz.WithListener(ln))
	h.Use(errors.ErrorHandler(isProd))
	h.Any("/*path", gw.Forward)

	go func() {
		_ = h.Run()
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.Shutdown(ctx)
	})

	return ln.Addr().String()
}

// testClient never reuses a connection. These tests each bind their own port
// and run in parallel, and a pooled connection is keyed by host:port — so after
// one test's server shuts down and its port is handed to another test, a keep-
// alive connection would deliver the request to the second gateway, whose
// routing table answers 404 for a path that is perfectly valid in the first.
// That makes a pooling artefact look exactly like a routing bug.
var testClient = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true},
}

// getJSON performs one GET and decodes the error body, so the assertions below
// are about what the client actually receives.
func getJSON(t *testing.T, addr, path, authorization string) (int, map[string]any) {
	t.Helper()

	code, body := doGet(t, addr, path, authorization)
	if body == nil {
		return code, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}

	return code, decoded
}

// doGet performs one GET and returns the status with the raw body.
func doGet(t *testing.T, addr, path, authorization string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	return resp.StatusCode, body
}

// assertErrorResponse checks the contract the client sees: the status that the
// recorded error's code maps to, and a body carrying that error's key.
func assertErrorResponse(t *testing.T, code int, body map[string]any, wantStatus int, wantKey string) {
	t.Helper()

	if code != wantStatus {
		t.Errorf("status = %d, want %d", code, wantStatus)
	}

	if got := body["key"]; got != wantKey {
		t.Errorf("key = %v, want %q", got, wantKey)
	}
}

func echoBackend(t *testing.T) *httptest.Server {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from-backend"))
	}))
	t.Cleanup(backend.Close)

	return backend
}

// TestErrorHandler_UnmatchedRoute covers the failure that never reaches a hook:
// there is nothing to forward to, and the client still gets the same shaped
// body as every other error.
func TestErrorHandler_UnmatchedRoute(t *testing.T) {
	t.Parallel()

	gw := buildGateway(t, hook.NewRegistry())
	addr := hertzAddrWithErrorHandler(t, gw, false)

	code, body := getJSON(t, addr, "/no/such/route", "")

	assertErrorResponse(t, code, body, http.StatusNotFound, "ROUTE_NOT_FOUND")
}

// TestErrorHandler_ProxyFailure covers a backend that could not be reached at
// all, which is the gateway's own problem rather than the backend's answer.
//
// The target carries its scheme deliberately. A bare "host:port" is not a URL:
// reverseproxy parses it as scheme "127.0.0.1" with an empty host, so the proxy
// ends up addressing the gateway's own listener and the request re-enters
// Forward with a mangled path — a 404 that looks like a routing bug and hides
// the proxy failure this test exists to check.
func TestErrorHandler_ProxyFailure(t *testing.T) {
	t.Parallel()

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/dead", methods: []string{"GET"}, target: "http://127.0.0.1:1",
	})
	addr := hertzAddrWithErrorHandler(t, gw, false)

	code, body := getJSON(t, addr, "/dead", "")

	assertErrorResponse(t, code, body, http.StatusBadGateway, "BAD_GATEWAY")
}

// TestErrorHandler_HookFailure covers a hook that returns an error rather than
// answering the client itself: the failure is the gateway's, so it is a 500.
func TestErrorHandler_HookFailure(t *testing.T) {
	t.Parallel()

	registry := hook.NewRegistry()
	registerHook(t, registry, &recordingHook{
		name: "explodes", stage: hook.PreRequest,
		execute: func(context.Context, *app.RequestContext) error {
			return errors.New("hook exploded")
		},
	})

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: echoBackend(t).URL,
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{{Name: "explodes"}},
		},
	})
	addr := hertzAddrWithErrorHandler(t, gw, false)

	code, body := getJSON(t, addr, "/ping", "")

	assertErrorResponse(t, code, body, http.StatusInternalServerError, "INTERNAL")
}

// TestAuthHook_ThroughErrorHandler drives the real auth hook, registered from a
// real config the way main.go registers it. It is the end-to-end test the hook
// was missing: an auth rejection has to reach the client as a 401 it can act
// on, not as a server fault, and the credentials the hook accepts have to be
// the ones an issuer actually produces.
func TestAuthHook_ThroughErrorHandler(t *testing.T) {
	t.Parallel()

	backend := echoBackend(t)

	authCfg := &config.Config{
		AppName: "gateway",
		Auth: config.Auth{
			AccessToken: config.AccessToken{
				Secret: testTokenSecret,
				TTL:    config.Duration(time.Hour),
			},
			RefreshToken: config.RefreshToken{
				Secret: testTokenSecret,
				TTL:    config.Duration(2 * time.Hour),
			},
		},
	}

	registry := hook.NewRegistry()
	if err := hooks.Register(registry, authCfg); err != nil {
		t.Fatalf("registering hooks: %v", err)
	}

	gw := buildGateway(t, registry, routeSpec{
		path: "/me", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{{Name: "paseto"}},
		},
	})
	addr := hertzAddrWithErrorHandler(t, gw, false)

	issuer, err := auth.NewLocalPaseto(auth.LocalPasetoConfig{
		Issuer:     "gateway",
		AccessKey:  []byte(testTokenSecret),
		RefreshKey: []byte(testTokenSecret),
		AccessTTL:  time.Hour,
		RefreshTTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("building the issuer: %v", err)
	}

	tokens, err := issuer.Generate("user-1", "gateway", nil)
	if err != nil {
		t.Fatalf("issuing a token: %v", err)
	}

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
		wantKey       string
	}{
		{name: "no header", wantStatus: http.StatusUnauthorized, wantKey: "UNAUTHORIZED"},
		{name: "wrong scheme", authorization: "Basic abc", wantStatus: http.StatusUnauthorized, wantKey: "UNAUTHORIZED"},
		{name: "garbage token", authorization: "Bearer not-a-token", wantStatus: http.StatusUnauthorized, wantKey: "UNAUTHORIZED"},
		{name: "valid token reaches the backend", authorization: "Bearer " + tokens.Access, wantStatus: http.StatusOK},
		{name: "lowercase scheme is accepted", authorization: "bearer " + tokens.Access, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, raw := doGet(t, addr, "/me", tt.authorization)

			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", code, tt.wantStatus)
			}

			if tt.wantKey == "" {
				return
			}

			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}

			if got := body["key"]; got != tt.wantKey {
				t.Errorf("key = %v, want %q", got, tt.wantKey)
			}
		})
	}
}

// TestErrorHandler_ProdHidesStackOnTheWire is the same production guarantee as
// the unit test, checked against a real response rather than a recorder: the
// stack is the one field that must not survive into production.
func TestErrorHandler_ProdHidesStackOnTheWire(t *testing.T) {
	t.Parallel()

	gw := buildGateway(t, hook.NewRegistry())
	addr := hertzAddrWithErrorHandler(t, gw, true)

	code, body := getJSON(t, addr, "/no/such/route", "")

	assertErrorResponse(t, code, body, http.StatusNotFound, "ROUTE_NOT_FOUND")

	if _, ok := body["stack"]; ok {
		t.Error("expected no stack field in production")
	}

	if len(body) != 1 {
		t.Errorf("expected only the key in production, got %v", body)
	}
}
