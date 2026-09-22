package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hertz "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	apperrors "github.com/maadiii/gogate/pkg/errors"
	concretehooks "github.com/maadiii/gogate/pkg/hooks"
)

// The tests in this file are the only ones that drive a real Hertz server
// over real TCP. That fidelity is the whole point: every other test in this
// package calls Gateway.Forward directly with a context built by
// ut.CreateUtRequestContext, and that helper constructs a brand-new engine
// and a brand-new RequestContext on every call. It therefore never exercises
// the request lifecycle that production actually runs - the real router, the
// real HTTP parser and writer, and, most importantly, Hertz's sync.Pool of
// RequestContexts, which recycles one context object across thousands of
// unrelated requests. Context bleed that only shows up under recycling is
// invisible to a test that mints a fresh context per request.

// hertzAddr boots a real Hertz server in front of gw on an OS-assigned port
// and returns its host:port. The server is shut down via t.Cleanup.
//
// Binding the listener ourselves (rather than letting Hertz pick the port)
// means the socket is already accepting by the time this returns, so callers
// can connect immediately without a readiness poll.
func hertzAddr(t *testing.T, gw *Gateway) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding listener: %v", err)
	}

	h := hertz.New(hertz.WithListener(ln))
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

func hertzAddrWithErrors(t *testing.T, gw *Gateway) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding listener: %v", err)
	}

	h := hertz.New(hertz.WithListener(ln))
	h.Use(apperrors.ErrorHandler(false))
	h.Any("/*path", gw.Forward)
	go func() { _ = h.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})

	return ln.Addr().String()
}

// TestE2E_ConcurrentMixedRoutes_NoCrossTalk runs concurrent traffic through a
// real server across exact routes, a wildcard route and several backends. No
// response may carry another backend's payload.
func TestE2E_ConcurrentMixedRoutes_NoCrossTalk(t *testing.T) {
	t.Parallel()

	const (
		iterations  = 600
		maxInFlight = 24
	)

	marker := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(name))
		}))
	}

	svcA := marker("payload-A")
	t.Cleanup(svcA.Close)
	svcB := marker("payload-B")
	t.Cleanup(svcB.Close)
	svcC := marker("payload-C")
	t.Cleanup(svcC.Close)

	gw := buildGateway(
		t, hook.NewRegistry(),
		routeSpec{path: "/api/exact", methods: []string{"GET"}, target: svcA.URL},
		routeSpec{path: "/api/wild/*", methods: []string{"GET"}, target: svcB.URL},
		routeSpec{path: "/api/other/*", methods: []string{"GET"}, target: svcC.URL},
	)

	addr := hertzAddr(t, gw)

	// Each request path must resolve to exactly the payload of the backend it
	// is routed to - including the wildcard routes, which share the wildcard
	// index and so are a plausible place for one route's target to be served
	// for another's.
	cases := []struct {
		path    string
		payload string
	}{
		{"/api/exact", "payload-A"},
		{"/api/wild/12345", "payload-B"},
		{"/api/wild/deeper/nested", "payload-B"},
		{"/api/other/thing", "payload-C"},
	}

	client := &http.Client{Timeout: 10 * time.Second}

	runConcurrently(iterations, maxInFlight, func(i int) {
		tc := cases[i%len(cases)]

		resp, err := client.Get("http://" + addr + tc.path) //nolint
		if err != nil {
			t.Errorf("GET %s: %v", tc.path, err)

			return
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("GET %s: reading body: %v", tc.path, err)

			return
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, body %q", tc.path, resp.StatusCode, body)

			return
		}

		if got := string(body); got != tc.payload {
			t.Errorf("GET %s: expected %q, got %q (CROSS-TALK)", tc.path, tc.payload, got)
		}
	})
}

// TestE2E_KeepAliveSequential_PooledContextNotTainted is the strongest
// context-recycling regression test in this package.
//
// It speaks raw HTTP/1.1 over a single TCP connection, so every request in the
// sequence is handled by the same server-side connection and its
// RequestContext is returned to Hertz's pool and handed straight back for the
// next one. The first request deliberately targets an unreachable backend and
// must fail with 502; every request after it targets a healthy backend and
// must succeed. If the failed request's state (the error the gateway stashes
// under proxyErrorKey) survived the reset, or were read from a stale context,
// the healthy requests would be wrongly turned into 502s.
func TestE2E_KeepAliveSequential_PooledContextNotTainted(t *testing.T) {
	t.Parallel()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("healthy-payload"))
	}))
	t.Cleanup(healthy.Close)

	// Bind then immediately release a port so nothing is listening on it;
	// connections are refused deterministically.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	gw := buildGateway(
		t, hook.NewRegistry(),
		routeSpec{path: "/healthy", methods: []string{"GET"}, target: healthy.URL},
		routeSpec{path: "/dead", methods: []string{"GET"}, target: deadURL},
	)

	addr := hertzAddr(t, gw)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dialing gateway: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("setting deadline: %v", err)
	}

	br := bufio.NewReader(conn)

	roundTrip := func(path string) (int, string) {
		t.Helper()

		req := fmt.Sprintf(
			"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n",
			path, addr,
		)
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("writing request for %s: %v", path, err)
		}

		resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
		if err != nil {
			t.Fatalf("reading response for %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading body for %s: %v", path, err)
		}

		return resp.StatusCode, string(body)
	}

	// 1. The failing request that seeds the context with a proxy error.
	if code, _ := roundTrip("/dead"); code != http.StatusBadGateway {
		t.Fatalf("expected the unreachable backend to produce 502, got %d", code)
	}

	// 2. Every subsequent request reuses the same connection, and therefore a
	//    recycled RequestContext. None may inherit the failure above.
	for i := range 10 {
		code, body := roundTrip("/healthy")

		if code != http.StatusOK {
			t.Errorf("request %d on the recycled connection: status %d (context bleed from the earlier failure)", i, code)

			continue
		}
		if body != "healthy-payload" {
			t.Errorf("request %d: expected body %q, got %q", i, "healthy-payload", body)
		}
	}
}

// TestE2E_PostBodyAndHeaders_RoundTrip verifies a request survives the real
// HTTP stack intact: method, body, and headers all reach the backend, and the
// backend's response comes back unmodified.
func TestE2E_PostBodyAndHeaders_RoundTrip(t *testing.T) {
	t.Parallel()

	const (
		requestBody  = `{"hello":"world"}`
		responseBody = `{"ok":true}`
	)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		if r.Method != http.MethodPost ||
			string(body) != requestBody ||
			r.Header.Get("X-Custom") != "custom-value" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(
				w,
				"unexpected: method=%s body=%q custom=%q",
				r.Method, body, r.Header.Get("X-Custom"),
			)

			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(backend.Close)

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/things", methods: []string{"POST"}, target: backend.URL,
	})

	addr := hertzAddr(t, gw)

	req, err := http.NewRequest(
		http.MethodPost,
		"http://"+addr+"/things",
		strings.NewReader(requestBody),
	)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("X-Custom", "custom-value")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /things: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := string(body); got != responseBody {
		t.Errorf("expected body %q, got %q", responseBody, got)
	}
}

// TestE2E_WildcardAndExactPrecedence_ThroughRealRouter covers route
// resolution end to end: an exact route wins over a wildcard route that also
// matches the same path, a wildcard route catches subpaths, and a wildcard
// prefix does not match the bare prefix itself.
func TestE2E_WildcardAndExactPrecedence_ThroughRealRouter(t *testing.T) {
	t.Parallel()

	marker := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(name))
		}))
	}

	exact := marker("exact")
	t.Cleanup(exact.Close)
	wild := marker("wild")
	t.Cleanup(wild.Close)

	gw := buildGateway(
		t, hook.NewRegistry(),
		routeSpec{path: "/api/users/special", methods: []string{"GET"}, target: exact.URL},
		routeSpec{path: "/api/users/*", methods: []string{"GET"}, target: wild.URL},
	)

	addr := hertzAddr(t, gw)
	client := &http.Client{Timeout: 10 * time.Second}

	for _, tc := range []struct {
		path string
		want string
	}{
		// Exact must win even though the wildcard also matches.
		{"/api/users/special", "exact"},
		{"/api/users/42", "wild"},
		{"/api/users/42/posts", "wild"},
	} {
		resp, err := client.Get("http://" + addr + tc.path) //nolint
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", tc.path, resp.StatusCode)

			continue
		}
		if got := string(body); got != tc.want {
			t.Errorf("GET %s: expected %q, got %q", tc.path, tc.want, got)
		}
	}

	// The wildcard prefix itself (no trailing segment) must not match.
	resp, err := client.Get("http://" + addr + "/api/users") //nolint
	if err != nil {
		t.Fatalf("GET /api/users: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected /api/users to be unmatched (404), got %d", resp.StatusCode)
	}
}

// TestE2E_AuthHook_AuthenticatesForwardsAndRejects drives a real client-facing
// gateway and a distinct real backend. It proves that auth runs before proxying,
// replaces forged identity headers, preserves the backend response, and blocks
// an invalid token before it can reach the backend.
func TestE2E_AuthHook_AuthenticatesForwardsAndRejects(t *testing.T) {
	const issuer = "auth-e2e"

	backendCalls := make(chan struct{}, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls <- struct{}{}
		if got := r.Header.Get("X-UserID"); got != "user-42" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "X-UserID = %q", got)
			return
		}
		if got := r.Header.Values("X-Roles"); len(got) != 2 || got[0] != "member" || got[1] != "billing" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "X-Roles = %v", got)
			return
		}
		if got := r.Header.Values("X-Perms"); len(got) != 1 || got[0] != "reports:read" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "X-Perms = %v", got)
			return
		}
		w.Header().Set("X-Backend", "authenticated")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(backend.Close)

	authConfig, paseto := newPublicPasetoFixture(t, issuer)
	cfg := &config.Config{Port: 8000, AppName: issuer, Auth: authConfig}
	registry := hook.NewRegistry()
	if err := concretehooks.Register(registry, cfg); err != nil {
		t.Fatalf("registering auth hook: %v", err)
	}
	gw := buildGateway(t, registry, routeSpec{
		path: "/private", methods: []string{http.MethodGet}, target: backend.URL,
		hooks: config.RouteHooks{PreRequest: config.HookRefList{{Name: "paseto"}}},
	})
	addr := hertzAddrWithErrors(t, gw)

	tokens, err := paseto.Generate("user-42", "", map[string]any{
		"roles": []string{"member", "billing"}, "perms": []string{"reports:read"},
	})
	if err != nil {
		t.Fatalf("generating access token: %v", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	request := func(authorization string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/private", nil)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", authorization)
		req.Header.Set("X-UserID", "forged-user")
		req.Header.Add("X-Roles", "admin")
		req.Header.Add("X-Perms", "everything:write")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("calling gateway: %v", err)
		}
		return resp
	}

	valid := request("Bearer " + tokens.Access)
	validBody, _ := io.ReadAll(valid.Body)
	_ = valid.Body.Close()
	if valid.StatusCode != http.StatusCreated || string(validBody) != `{"accepted":true}` || valid.Header.Get("X-Backend") != "authenticated" {
		t.Fatalf("valid request: status=%d body=%q backend=%q", valid.StatusCode, validBody, valid.Header.Get("X-Backend"))
	}

	invalid := request("Bearer not-a-paseto-token")
	invalidBody, _ := io.ReadAll(invalid.Body)
	_ = invalid.Body.Close()
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid token: status=%d body=%q, want 401", invalid.StatusCode, invalidBody)
	}
	if !strings.Contains(string(invalidBody), `"key":"UNAUTHORIZED"`) {
		t.Fatalf("invalid token: body=%q, want the shaped UNAUTHORIZED error", invalidBody)
	}

	missing := request("")
	missingBody, _ := io.ReadAll(missing.Body)
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusUnauthorized || !strings.Contains(string(missingBody), `"key":"UNAUTHORIZED"`) {
		t.Fatalf("missing token: status=%d body=%q, want shaped 401", missing.StatusCode, missingBody)
	}

	select {
	case <-backendCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("valid request never reached backend")
	}
	select {
	case <-backendCalls:
		t.Fatal("invalid token reached backend")
	default:
	}
}

// TestE2E_PASETOAuth_ConcurrentIdentitiesStayIsolated combines the two
// production risks that must never be tested separately only: Hertz's pooled
// inbound RequestContexts under real TCP concurrency, and one shared auth
// hook/reverse-proxy/client forwarding distinct authenticated identities.
func TestE2E_PASETOAuth_ConcurrentIdentitiesStayIsolated(t *testing.T) {
	t.Parallel()

	const (
		issuer      = "paseto-concurrency-e2e"
		iterations  = 600
		maxInFlight = 24
	)

	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)

		roles := r.Header.Values("X-Roles")
		permissions := r.Header.Values("X-Perms")
		if len(roles) != 1 || len(permissions) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "roles=%v permissions=%v", roles, permissions)

			return
		}

		_, _ = fmt.Fprintf(w, "%s|%s|%s", r.Header.Get("X-UserID"), roles[0], permissions[0])
	}))
	t.Cleanup(backend.Close)

	authConfig, issuerPaseto := newPublicPasetoFixture(t, issuer)
	cfg := &config.Config{Port: 8000, AppName: issuer, Auth: authConfig}
	registry := hook.NewRegistry()
	if err := concretehooks.Register(registry, cfg); err != nil {
		t.Fatalf("registering paseto hook: %v", err)
	}
	gw := buildGateway(t, registry, routeSpec{
		path: "/private", methods: []string{http.MethodGet}, target: backend.URL,
		hooks: config.RouteHooks{PreRequest: config.HookRefList{{Name: "paseto"}}},
	})
	addr := hertzAddrWithErrors(t, gw)

	tokens := make([]string, iterations)
	for i := range iterations {
		userID := fmt.Sprintf("user-%d", i)
		issued, err := issuerPaseto.Generate(userID, "", map[string]any{
			"roles": []string{fmt.Sprintf("role-%d", i)},
			"perms": []string{fmt.Sprintf("resource:%d:read", i)},
		})
		if err != nil {
			t.Fatalf("issuing token %d: %v", i, err)
		}
		tokens[i] = issued.Access
	}

	client := &http.Client{Timeout: 10 * time.Second}
	runConcurrently(iterations, maxInFlight, func(i int) {
		userID := fmt.Sprintf("user-%d", i)
		want := fmt.Sprintf("%s|role-%d|resource:%d:read", userID, i, i)

		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/private", nil)
		if err != nil {
			t.Errorf("request %d: building request: %v", i, err)

			return
		}
		req.Header.Set("Authorization", "Bearer "+tokens[i])
		// A distinct forged value on every request makes a leaked or un-cleared
		// header unmistakable at the backend.
		req.Header.Set("X-UserID", "forged-"+userID)
		req.Header.Set("X-Roles", "forged-role")
		req.Header.Set("X-Perms", "forged:permission")

		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("request %d: calling gateway: %v", i, err)

			return
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Errorf("request %d: reading response: %v", i, readErr)

			return
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: status=%d body=%q", i, resp.StatusCode, body)

			return
		}
		if got := string(body); got != want {
			t.Errorf("request %d: response=%q, want %q (identity cross-talk)", i, got, want)
		}
	})

	if got := backendCalls.Load(); got != iterations {
		t.Errorf("backend calls = %d, want %d", got, iterations)
	}
}
