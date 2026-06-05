// Package api provides an optional HTTP server for managing tracked packages
// and inspecting extraction state. Start it by setting api_host and api_port
// in config.yaml. If api_port is 0 or absent, no server is started.
//
// Authentication: set api_token in config.yaml to require a Bearer token on all
// non-health endpoints. Strongly recommended â€” this service is the APK source
// for production MDM fleets; anyone who can add packages here can influence
// what gets deployed to managed devices.
//
// The server binds to 127.0.0.1 by default. Do not expose it to untrusted
// networks â€” the token provides authentication but not transport encryption.
//
// Endpoints:
//
//	GET    /health              service liveness (no auth required)
//	GET    /packages            list all tracked packages with version info
//	POST   /packages            add a package     body: {"name":"com.example.app"}
//	GET    /packages/{name}     get one package's version info
//	DELETE /packages/{name}     remove a package
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/richardcornall/play-apk-distributor/packages"
	"github.com/richardcornall/play-apk-distributor/store"
)

const maxBodyBytes = 1024 // package names are short â€” reject anything larger

// Server is the HTTP API server.
type Server struct {
	pkgs *packages.Manager
	st   *store.Store
	srv  *http.Server
}

// New creates a Server that listens on addr.
// addr should be "127.0.0.1:<port>" for local-only access.
// token is the Bearer token required on all non-health endpoints; pass "" to
// disable token auth (not recommended for production â€” log a warning before calling).
func New(addr, token string, pkgs *packages.Manager, st *store.Store) *Server {
	s := &Server{pkgs: pkgs, st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /packages", s.listPackages)
	mux.HandleFunc("POST /packages", s.addPackage)
	mux.HandleFunc("GET /packages/{name}", s.getPackage)
	mux.HandleFunc("DELETE /packages/{name}", s.removePackage)
	s.srv = &http.Server{
		Addr:         addr,
		Handler:      authMiddleware(token, mux),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	return s
}

// Start begins serving. Blocks until the server closes.
func (s *Server) Start() error {
	slog.Info("api server listening", "addr", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server within the deadline of ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// ServeHTTP implements http.Handler so tests can drive the server without binding a port.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.srv.Handler.ServeHTTP(w, r)
}

// authMiddleware requires a valid Bearer token on all endpoints except /health.
// When token is empty all requests are allowed through (with a startup warning).
// Constant-time comparison prevents timing-based token enumeration.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + token
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			writeError(w, http.StatusUnauthorized, "Authorization: Bearer <token> required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- handlers ---

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listPackages(w http.ResponseWriter, r *http.Request) {
	names := s.pkgs.List()
	items := make([]packageInfo, 0, len(names))
	for _, name := range names {
		items = append(items, s.packageInfo(name))
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": items})
}

func (s *Server) addPackage(w http.ResponseWriter, r *http.Request) {
	// Accept "application/json" and "application/json; charset=utf-8" etc.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := packages.ValidatePackageName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.pkgs.Add(req.Name); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	slog.Info("package added via api", "package", req.Name)
	writeJSON(w, http.StatusCreated, s.packageInfo(req.Name))
}

func (s *Server) getPackage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := packages.ValidatePackageName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.pkgs.Has(name) {
		writeError(w, http.StatusNotFound, "package not tracked")
		return
	}
	writeJSON(w, http.StatusOK, s.packageInfo(name))
}

func (s *Server) removePackage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := packages.ValidatePackageName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.pkgs.Remove(name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	slog.Info("package removed via api", "package", name)
	w.WriteHeader(http.StatusNoContent)
}

// --- response types ---

type packageInfo struct {
	Name        string `json:"name"`
	VersionCode int    `json:"version_code,omitempty"`
	VersionName string `json:"version_name,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func (s *Server) packageInfo(name string) packageInfo {
	info := packageInfo{Name: name}
	if e, ok := s.st.GetLastKnown(name); ok {
		info.VersionCode = e.VersionCode
		info.VersionName = e.VersionName
		info.UpdatedAt = e.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return info
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck â€” client disconnect is not actionable
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
