package token_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdbbot/internal/token"
)

// fakeJWT returns a minimal JWT string (header.payload.fakesig) with a real
// exp claim so accessExp can parse it. The signature is not valid — only the
// structure and claims matter for tests.
func fakeJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"exp":%d,"iat":%d,"token_type":"access_token"}`,
			exp.Unix(), time.Now().Unix()),
	))
	return header + "." + payload + ".fakesig"
}

// stubServer is a minimal PDB API stub for refresh + two well-known endpoints.
// currentRT is the only valid refresh token at any moment; it rotates on each
// successful refresh call.
type stubServer struct {
	mu           sync.Mutex
	currentRT    string
	rotations    int
	RefreshCount atomic.Int64
}

func (s *stubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v2/token/refresh":
		s.handleRefresh(w, r)
	case "/api/v2/users/4885554/irl_preview":
		jsonOK(w, `{"data":{},"error":{"code":"S20000"}}`)
	case "/api/v2/im/channels/list":
		jsonOK(w, `{"data":{"channels":[]},"error":{"code":"S20000"}}`)
	default:
		http.NotFound(w, r)
	}
}

func (s *stubServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.RefreshCount.Add(1)

	auth := r.Header.Get("Authorization")
	if len(auth) <= 7 {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	presented := auth[7:] // strip "Bearer "

	s.mu.Lock()
	valid := presented == s.currentRT
	if valid {
		s.rotations++
		s.currentRT = fmt.Sprintf("rt-rotated-%d", s.rotations)
		rt := s.currentRT
		s.mu.Unlock()

		at := fakeJWT(time.Now().Add(72 * time.Hour))
		expMs := time.Now().Add(72*time.Hour).Unix() * 1000
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"accessToken":  at,
				"refreshToken": rt,
				"expireAt":     expMs,
			},
			"error": map[string]any{"code": "S20000"},
		})
	} else {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":{"code":"E40101"}}`, http.StatusUnauthorized)
	}
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, body)
}

// newStub starts a stub server seeded with initialRT and returns it alongside
// an *http.Client that redirects all requests to the stub.
func newStub(t *testing.T, initialRT string) (*stubServer, *httptest.Server, *http.Client) {
	t.Helper()
	s := &stubServer{currentRT: initialRT}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	client := clientFor(srv)
	return s, srv, client
}

// clientFor returns an http.Client that rewrites the host of every request to
// point at srv (transparently redirecting PDB API calls to the stub).
func clientFor(srv *httptest.Server) *http.Client {
	host := srv.Listener.Addr().String()
	return &http.Client{
		Transport: &hostRewrite{host: host, inner: http.DefaultTransport},
		Timeout:   10 * time.Second,
	}
}

type hostRewrite struct {
	host  string
	inner http.RoundTripper
}

func (h *hostRewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = h.host
	return h.inner.RoundTrip(r2)
}

// writeState writes s as JSON to path.
func writeState(t *testing.T, path string, s token.TokenState) {
	t.Helper()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, path string) token.TokenState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s token.TokenState
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	return s
}

// --- Test 1: cold start with empty access token forces refresh ---

func TestColdStartRefresh(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	const seedRT = "seed-rt-coldstart"

	stub, _, client := newStub(t, seedRT)
	writeState(t, statePath, token.TokenState{
		AccessToken:  "",
		RefreshToken: seedRT,
		ExpireAt:     0,
		DeviceID:     "266729c8629762eb",
	})

	m, err := token.Load(statePath, client)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	tok, err := m.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if tok == "" {
		t.Fatal("got empty access token")
	}
	if n := stub.RefreshCount.Load(); n != 1 {
		t.Fatalf("want 1 refresh call, got %d", n)
	}

	saved := readState(t, statePath)
	if saved.AccessToken == "" {
		t.Error("access token not persisted")
	}
	if saved.RefreshToken == seedRT {
		t.Error("refresh token was not rotated in state file")
	}
	if saved.ExpireAt == 0 {
		t.Error("expire_at not persisted")
	}
}

// --- Test 2: authenticated call succeeds ---

func TestAuthenticatedCall(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	const seedRT = "seed-rt-authcall"

	_, _, client := newStub(t, seedRT)
	writeState(t, statePath, token.TokenState{
		AccessToken:  "",
		RefreshToken: seedRT,
		ExpireAt:     0,
		DeviceID:     "266729c8629762eb",
	})

	m, err := token.Load(statePath, client)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	req, err := token.NewAPIRequest(context.Background(), "GET", "/users/4885554/irl_preview", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := m.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
}

// --- Test 3: proactive expiry triggers exactly one refresh ---

func TestProactiveExpiry(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	const seedRT = "seed-rt-proactive"

	stub, _, client := newStub(t, seedRT)
	// Write state with a valid (non-empty) but expired access token.
	writeState(t, statePath, token.TokenState{
		AccessToken:  fakeJWT(time.Now().Add(10 * time.Minute)), // not expired yet
		RefreshToken: seedRT,
		ExpireAt:     time.Now().Add(10 * time.Minute).Unix(),
		DeviceID:     "266729c8629762eb",
	})

	m, err := token.Load(statePath, client)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	// Force the in-memory token to look expired.
	m.ForceExpire()

	tok, err := m.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if tok == "" {
		t.Fatal("got empty token after proactive refresh")
	}
	if n := stub.RefreshCount.Load(); n != 1 {
		t.Fatalf("want 1 refresh, got %d", n)
	}
}

// --- Test 4: 20 concurrent AccessToken calls → exactly 1 network refresh ---

func TestSingleFlightRefresh(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	const seedRT = "seed-rt-singleflight"

	stub, _, client := newStub(t, seedRT)
	writeState(t, statePath, token.TokenState{
		AccessToken:  "",
		RefreshToken: seedRT,
		ExpireAt:     0,
		DeviceID:     "266729c8629762eb",
	})

	m, err := token.Load(statePath, client)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	const N = 20
	tokens := make([]string, N)
	errs := make([]error, N)

	var wg sync.WaitGroup
	var gate sync.WaitGroup
	gate.Add(1)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			gate.Wait()
			tokens[i], errs[i] = m.AccessToken(context.Background())
		}(i)
	}
	gate.Done()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: %v", i, e)
		}
	}
	for i, tok := range tokens {
		if tok == "" {
			t.Errorf("goroutine %d: empty token", i)
		}
	}
	if n := stub.RefreshCount.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh network call, got %d", n)
	}

	saved := readState(t, statePath)
	if saved.RefreshToken == "" {
		t.Error("refresh token missing from state file after concurrent refresh")
	}
}

// --- Test 5: reactive 401 — bad in-memory token causes refresh + retry ---

func TestReactive401(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	const seedRT = "seed-rt-reactive"

	// Start a fresh stub and get its underlying *stubServer so we can wire
	// a custom endpoint mux on top.
	stub := &stubServer{currentRT: seedRT}

	var endpointCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/token/refresh", stub.handleRefresh)
	mux.HandleFunc("/api/v2/users/4885554/irl_preview", func(w http.ResponseWriter, r *http.Request) {
		n := endpointCalls.Add(1)
		if n == 1 {
			// first call: reject to exercise the reactive path
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		jsonOK(w, `{"data":{},"error":{"code":"S20000"}}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := clientFor(srv)

	// Write a "valid" (future-dated) access token so the proactive check
	// passes — we want to hit the reactive path, not the proactive one.
	writeState(t, statePath, token.TokenState{
		AccessToken:  fakeJWT(time.Now().Add(72 * time.Hour)),
		RefreshToken: seedRT,
		ExpireAt:     time.Now().Add(72 * time.Hour).Unix(),
		DeviceID:     "266729c8629762eb",
	})

	m, err := token.Load(statePath, client)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	req, err := token.NewAPIRequest(context.Background(), "GET", "/users/4885554/irl_preview", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := m.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200 after retry, got %d: %s", resp.StatusCode, body)
	}
	if endpointCalls.Load() != 2 {
		t.Errorf("want 2 endpoint calls (401 then 200), got %d", endpointCalls.Load())
	}
	if stub.RefreshCount.Load() != 1 {
		t.Errorf("want 1 refresh call, got %d", stub.RefreshCount.Load())
	}
}
