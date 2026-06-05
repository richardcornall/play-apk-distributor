package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/richardcornall/play-apk-distributor/api"
	"github.com/richardcornall/play-apk-distributor/packages"
	"github.com/richardcornall/play-apk-distributor/store"
)

func newTestServer(t *testing.T) (*api.Server, *packages.Manager, *store.Store) {
	t.Helper()
	return newTestServerWithToken(t, "")
}

func newTestServerWithToken(t *testing.T, token string) (*api.Server, *packages.Manager, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	pkgs, err := packages.New(dir)
	if err != nil {
		t.Fatalf("packages.New: %v", err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := api.New("127.0.0.1:0", token, pkgs, st)
	return srv, pkgs, st
}

// roundTrip sends a request through the server's handler (without binding a port).
func roundTrip(t *testing.T, srv *api.Server, method, path string, body []byte, ct string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, path, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, path, nil)
	}
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// --- /health ---

func TestHealth(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodGet, "/health", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("health body = %v", body)
	}
}

// --- GET /packages ---

func TestListPackages_Empty(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodGet, "/packages", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /packages = %d, want 200", w.Code)
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	pkgs := body["packages"].([]any)
	if len(pkgs) != 0 {
		t.Errorf("want empty list, got %v", pkgs)
	}
}

func TestListPackages_ShowsAdded(t *testing.T) {
	srv, pkgs, _ := newTestServer(t)
	_ = pkgs.Add("com.example.app")
	w := roundTrip(t, srv, http.MethodGet, "/packages", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /packages = %d", w.Code)
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	list := body["packages"].([]any)
	if len(list) != 1 {
		t.Errorf("want 1 package, got %d", len(list))
	}
}

// --- POST /packages ---

func TestAddPackage_Success(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"name": "com.example.new"})
	w := roundTrip(t, srv, http.MethodPost, "/packages", body, "application/json")
	if w.Code != http.StatusCreated {
		t.Errorf("POST /packages = %d, want 201; body: %s", w.Code, w.Body)
	}
}

func TestAddPackage_MissingContentType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"name": "com.example.app"})
	w := roundTrip(t, srv, http.MethodPost, "/packages", body, "")
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415, got %d", w.Code)
	}
}

func TestAddPackage_InvalidName(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"name": "notapackage"})
	w := roundTrip(t, srv, http.MethodPost, "/packages", body, "application/json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAddPackage_Duplicate(t *testing.T) {
	srv, pkgs, _ := newTestServer(t)
	_ = pkgs.Add("com.example.app")
	body, _ := json.Marshal(map[string]string{"name": "com.example.app"})
	w := roundTrip(t, srv, http.MethodPost, "/packages", body, "application/json")
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

func TestAddPackage_EmptyName(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"name": ""})
	w := roundTrip(t, srv, http.MethodPost, "/packages", body, "application/json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAddPackage_OversizedBody(t *testing.T) {
	srv, _, _ := newTestServer(t)
	huge := bytes.Repeat([]byte("x"), 2048)
	w := roundTrip(t, srv, http.MethodPost, "/packages", huge, "application/json")
	if w.Code == http.StatusCreated {
		t.Error("oversized body should not result in 201")
	}
}

// --- GET /packages/{name} ---

func TestGetPackage_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodGet, "/packages/com.example.app", nil, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetPackage_Found(t *testing.T) {
	srv, pkgs, _ := newTestServer(t)
	_ = pkgs.Add("com.example.app")
	w := roundTrip(t, srv, http.MethodGet, "/packages/com.example.app", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestGetPackage_InvalidName(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodGet, "/packages/not-a-package!!!", nil, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid name, got %d", w.Code)
	}
}

// --- DELETE /packages/{name} ---

func TestDeletePackage_Success(t *testing.T) {
	srv, pkgs, _ := newTestServer(t)
	_ = pkgs.Add("com.example.app")
	w := roundTrip(t, srv, http.MethodDelete, "/packages/com.example.app", nil, "")
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestDeletePackage_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodDelete, "/packages/com.example.app", nil, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDeletePackage_InvalidName(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodDelete, "/packages/bad!!!", nil, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- authentication ---

func TestAuth_NoTokenAllowsAll(t *testing.T) {
	// No token configured â€” all requests pass through.
	srv, _, _ := newTestServerWithToken(t, "")
	w := roundTrip(t, srv, http.MethodGet, "/packages", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("no-token server should allow unauthenticated requests, got %d", w.Code)
	}
}

func TestAuth_ValidTokenAllows(t *testing.T) {
	const token = "this-is-a-test-token-that-is-long-enough"
	srv, _, _ := newTestServerWithToken(t, token)
	req, _ := http.NewRequest(http.MethodGet, "/packages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("valid token: want 200, got %d", w.Code)
	}
}

func TestAuth_MissingTokenRejects(t *testing.T) {
	srv, _, _ := newTestServerWithToken(t, "this-is-a-test-token-that-is-long-enough")
	w := roundTrip(t, srv, http.MethodGet, "/packages", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing token: want 401, got %d", w.Code)
	}
}

func TestAuth_WrongTokenRejects(t *testing.T) {
	srv, _, _ := newTestServerWithToken(t, "this-is-a-test-token-that-is-long-enough")
	req, _ := http.NewRequest(http.MethodGet, "/packages", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: want 401, got %d", w.Code)
	}
}

func TestAuth_HealthBypassesAuth(t *testing.T) {
	// /health must work without a token so monitoring can probe liveness.
	srv, _, _ := newTestServerWithToken(t, "this-is-a-test-token-that-is-long-enough")
	w := roundTrip(t, srv, http.MethodGet, "/health", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("/health without token: want 200, got %d", w.Code)
	}
}

func TestAuth_TimingConstant(t *testing.T) {
	// A wrong token and a missing token should both return 401.
	// We can't measure timing in a unit test, but we can assert both reject.
	srv, _, _ := newTestServerWithToken(t, "this-is-a-test-token-that-is-long-enough")
	for _, authHeader := range []string{"", "Bearer ", "Bearer x", "Bearer wrong"} {
		req, _ := http.NewRequest(http.MethodGet, "/packages", nil)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("auth header %q: want 401, got %d", authHeader, w.Code)
		}
	}
}

// --- response shape ---

func TestResponseContentType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	w := roundTrip(t, srv, http.MethodGet, "/health", nil, "")
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
