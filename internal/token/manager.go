package token

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/sync/singleflight"
)

const (
	BaseURL   = "https://api.personality-database.com/api/v2"
	XDevice   = "eyJPUyI6ImFuZHJvaWQiLCJBcHAtVmVyc2lvbiI6IjIuMTIxLjEyIiwiQXBwLUJ1aWxkTm8iOiIyNTYyIiwiTWFya2V0IjoiR29vZ2xlUGxheSIsIkJyYW5kIjoid2F5ZHJvaWQiLCJNb2RlbCI6IldheURyb2lkIHg4Nl82NCBEZXZpY2UiLCJCdW5kbGVJRCI6InBkYi5hcHAiLCJNYW51ZmFjdHVyZXIiOiJXYXlkcm9pZCIsIk9TLVZlcnNpb24iOiIxMyIsIlNESy1WZXJzaW9uIjoiMzMiLCJYLVBEQi1EZXZpY2UtSUQiOiIyNjY3MjljODYyOTc2MmViIiwiVGllciI6ImhpZ2giLCJSYW0iOiI0NzM1Nk1CIiwiQ3B1IjoidW5rbm93biIsIkNsaWVudCI6IlBkYi1DbGFzc2ljIn0="
	UserAgent = "PBD-Android 2.121.12(2562)"
)

// ErrReauthRequired means the refresh token was rejected. Only a human
// Google re-login can recover from this.
var ErrReauthRequired = errors.New("refresh rejected: re-seed via Google sign-in")

type TokenState struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpireAt     int64  `json:"expire_at"` // unix seconds (from JWT exp claim)
	DeviceID     string `json:"device_id"`
}

type Manager struct {
	path  string
	mu    sync.RWMutex
	state TokenState
	sf    singleflight.Group
	lock  *flock.Flock
	hc    *http.Client
	skew  time.Duration
}

// Load opens the state file and acquires an exclusive process lock.
// A second concurrent process gets an error rather than blocking.
func Load(path string, client *http.Client) (*Manager, error) {
	lk := flock.New(path + ".lock")
	ok, err := lk.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if !ok {
		return nil, errors.New("another pdbbot instance holds the token lock")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		lk.Unlock()
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s TokenState
	if err := json.Unmarshal(b, &s); err != nil {
		lk.Unlock()
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.RefreshToken == "" {
		lk.Unlock()
		return nil, errors.New("no refresh token in state file")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Manager{
		path:  path,
		state: s,
		lock:  lk,
		hc:    client,
		skew:  5 * time.Minute,
	}, nil
}

func (m *Manager) Close() error {
	return m.lock.Unlock()
}

// AccessToken returns a valid access token, proactively refreshing when
// within the skew window of expiry.
func (m *Manager) AccessToken(ctx context.Context) (string, error) {
	m.mu.RLock()
	tok, exp := m.state.AccessToken, m.state.ExpireAt
	m.mu.RUnlock()
	if tok != "" && time.Until(time.Unix(exp, 0)) > m.skew {
		return tok, nil
	}
	ns, err := m.doRefresh(ctx)
	if err != nil {
		return "", err
	}
	return ns.AccessToken, nil
}

// Do executes req with a valid access token, retrying once on 401.
// req must be built with a bytes.Reader body (which auto-sets GetBody) so
// the retry can rewind it.
func (m *Manager) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	tok, err := m.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	ApplyHeaders(req, tok)

	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	// 401: if another goroutine already refreshed, the in-memory token will
	// have changed — skip the refresh and just retry with the current token.
	m.mu.RLock()
	current := m.state.AccessToken
	m.mu.RUnlock()
	if current == tok {
		if _, err := m.doRefresh(ctx); err != nil {
			return nil, err
		}
	}
	newTok, err := m.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req2 := req.Clone(ctx)
	if req.GetBody != nil {
		if req2.Body, err = req.GetBody(); err != nil {
			return nil, fmt.Errorf("re-body: %w", err)
		}
	}
	ApplyHeaders(req2, newTok)
	return m.hc.Do(req2)
}

// SetSkew overrides the proactive refresh window (default 5m). Tests use this.
func (m *Manager) SetSkew(d time.Duration) {
	m.skew = d
}

// ForceExpire sets in-memory expiry to now for testing.
func (m *Manager) ForceExpire() {
	m.mu.Lock()
	m.state.ExpireAt = time.Now().Unix() - 1
	m.mu.Unlock()
}

func (m *Manager) doRefresh(ctx context.Context) (TokenState, error) {
	v, err, _ := m.sf.Do("refresh", func() (any, error) {
		m.mu.RLock()
		rt := m.state.RefreshToken
		dev := m.state.DeviceID
		m.mu.RUnlock()

		req, err := http.NewRequestWithContext(ctx, "POST", BaseURL+"/token/refresh", nil)
		if err != nil {
			return TokenState{}, err
		}
		// Refresh endpoint takes the refresh token in Authorization, not the access token.
		ApplyHeaders(req, rt)

		resp, err := m.hc.Do(req)
		if err != nil {
			return TokenState{}, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			return TokenState{}, ErrReauthRequired
		}
		// API returns 201 on successful refresh (observed in capture).
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return TokenState{}, fmt.Errorf("refresh: status %d", resp.StatusCode)
		}

		var out struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpireAt     int64  `json:"expireAt"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return TokenState{}, fmt.Errorf("refresh decode: %w", err)
		}
		if out.Data.AccessToken == "" || out.Data.RefreshToken == "" {
			return TokenState{}, errors.New("refresh: empty tokens in response")
		}

		exp, ok := accessExp(out.Data.AccessToken)
		if !ok {
			// expireAt from the API is milliseconds; convert to seconds.
			exp = out.Data.ExpireAt / 1000
		}

		ns := TokenState{
			AccessToken:  out.Data.AccessToken,
			RefreshToken: out.Data.RefreshToken,
			ExpireAt:     exp,
			DeviceID:     dev,
		}

		// PERSIST BEFORE COMMIT. The old refresh token is dead server-side
		// the moment the 200/201 arrived. If persist fails, stash the new
		// refresh token somewhere recoverable.
		if err := persist(m.path, ns); err != nil {
			_ = os.WriteFile(m.path+".EMERGENCY", []byte(ns.RefreshToken), 0600)
			return TokenState{}, fmt.Errorf(
				"CRITICAL: persist failed after rotation; new refresh token in %s.EMERGENCY: %w",
				m.path, err,
			)
		}

		m.mu.Lock()
		m.state = ns
		m.mu.Unlock()
		return ns, nil
	})
	if err != nil {
		return TokenState{}, err
	}
	return v.(TokenState), nil
}

// accessExp decodes the exp claim from a JWT payload without verifying signature.
func accessExp(jwt string) (int64, bool) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return 0, false
	}
	return claims.Exp, true
}

// persist writes state atomically: temp-file + fsync + rename (same directory).
func persist(path string, s TokenState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tok-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ApplyHeaders sets all required PDB headers on req.
// authToken is the access token for normal API calls, or the refresh token
// when calling /token/refresh.
func ApplyHeaders(req *http.Request, authToken string) {
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("X-Device", XDevice)
	req.Header.Set("X-Locale", "en")
	req.Header.Set("X-Region", "US")
	req.Header.Set("X-Lang", "en")
	req.Header.Set("X-Regions", `["US","",""]`)
	req.Header.Set("X-TZ-Database-Name", "GMT")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json")
}

// NewAPIRequest builds a request targeting BaseURL+path with a body.
// Uses bytes.NewReader so GetBody is wired automatically for Do's retry path.
func NewAPIRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var req *http.Request
	var err error
	if len(body) > 0 {
		req, err = http.NewRequestWithContext(ctx, method, BaseURL+path, bytes.NewReader(body))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, BaseURL+path, nil)
	}
	return req, err
}
