package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/nomad"
	"github.com/rselbach/nomadl/internal/store"
	"github.com/rselbach/nomadl/web"
)

type Server struct {
	store         *store.Store
	nomad         *nomad.Client
	mux           *http.ServeMux
	settingsStore appconfig.Store
	ingest        *ingester
}

func New(dbPath, nomadAddr string, ingestCfg IngestConfig, settingsStore appconfig.Store) (*Server, error) {
	st, err := store.New(dbPath)
	if err != nil {
		return nil, err
	}
	if ingestCfg.ResetOnStart {
		if err := st.Clear(context.Background()); err != nil {
			if closeErr := st.Close(); closeErr != nil {
				return nil, fmt.Errorf("reset database: %w; close store: %v", err, closeErr)
			}
			return nil, fmt.Errorf("reset database: %w", err)
		}
	}

	nc, err := nomad.NewClient(nomadAddr)
	if err != nil {
		if closeErr := st.Close(); closeErr != nil {
			return nil, fmt.Errorf("create nomad client: %w; close store: %v", err, closeErr)
		}
		return nil, err
	}

	s := &Server{
		store:         st,
		nomad:         nc,
		mux:           http.NewServeMux(),
		settingsStore: settingsStore,
		ingest:        newIngester(nc, st, ingestCfg),
	}

	s.routes()
	s.ingest.start()

	return s, nil
}

func (s *Server) NomadAddr() string {
	return s.nomad.Address()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.Handle("GET /", staticFiles())
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("POST /api/settings", s.handleSaveSettings)
	s.mux.HandleFunc("GET /api/query", s.handleQuery)
	s.mux.HandleFunc("GET /api/context", s.handleContext)
	s.mux.HandleFunc("GET /api/live", s.handleLive)
	s.mux.HandleFunc("GET /api/query/select", s.handleSelect)
	s.mux.HandleFunc("GET /api/query/filter", s.handleFilter)
	s.mux.HandleFunc("GET /api/query-suggestions", s.handleQuerySuggestions)
	s.mux.HandleFunc("POST /api/clear", s.handleClear)
}

// Serve serves on ln until ctx is cancelled, then shuts down the HTTP
// server, stops the ingester, and closes the store. The caller owns
// binding the listener (so it can retry ports); Serve owns closing it.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpServer := &http.Server{
		Handler: s.handler(ln.Addr().String()),
		// Derive request contexts from ctx so long-lived SSE handlers
		// exit promptly on shutdown instead of holding Shutdown open.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(ln) }()

	select {
	case err := <-serveErr:
		if closeErr := s.Close(); closeErr != nil {
			return fmt.Errorf("%w; close server: %v", err, closeErr)
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
		if err := httpServer.Close(); err != nil {
			slog.Warn("http close", "err", err)
		}
	}
	return s.Close()
}

// handler wraps the routes with the request guards. Cross-origin
// protection rejects state-changing requests that a foreign page sends
// from the user's browser; guardLoopback covers DNS rebinding.
func (s *Server) handler(listenAddr string) http.Handler {
	return guardLoopback(listenAddr, http.NewCrossOriginProtection().Handler(s.mux))
}

// guardLoopback rejects requests whose Host header is not a local name
// when the server is bound to a loopback address. Browsers enforce
// same-origin for reading responses, but a DNS-rebinding page could
// still drive state-changing endpoints like /api/clear without this.
func guardLoopback(addr string, next http.Handler) http.Handler {
	listenHost := hostOnly(addr)
	if !isLocalHostname(listenHost) {
		// Explicitly bound to a non-loopback address: the user opted
		// into network exposure, so any Host is acceptable.
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHostname(hostOnly(r.Host)) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.Trim(hostport, "[]")
	}
	return host
}

func isLocalHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Close stops ingestion, waiting for followers to exit, and closes the
// store.
func (s *Server) Close() error {
	s.ingest.stop()
	return s.store.Close()
}

// saveSettings persists settings and applies the ingest allowlist.
func (s *Server) saveSettings(settings appconfig.Settings) error {
	settings.IngestServices = cleanServiceList(settings.IngestServices)
	settings.TraceFields = cleanServiceList(settings.TraceFields)
	if err := s.settingsStore.Save(settings); err != nil {
		return err
	}
	s.ingest.setServices(settings.IngestServices)
	return nil
}

func cleanServiceList(services []string) []string {
	cleaned := make([]string, 0, len(services))
	seen := make(map[string]struct{}, len(services))
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service == "" {
			continue
		}
		if _, ok := seen[service]; ok {
			continue
		}
		seen[service] = struct{}{}
		cleaned = append(cleaned, service)
	}
	return cleaned
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := web.Files.ReadFile("index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(page); err != nil {
		slog.Warn("write index", "err", err)
	}
}

// staticFiles serves the embedded UI modules. no-cache makes the browser
// revalidate, so a new binary's UI is picked up on reload.
func staticFiles() http.Handler {
	files := http.FileServerFS(web.Files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
