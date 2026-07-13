package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/nomad"
	"github.com/rselbach/nomadl/internal/store"
	"github.com/rselbach/nomadl/web"
)

type Server struct {
	store            *store.Store
	nomad            *nomad.Client
	mux              *http.ServeMux
	settingsStore    appconfig.Store
	settingsMu       sync.RWMutex
	ingestServices   []string
	priorityServices []string

	ingestMu     sync.Mutex
	ingestCfg    IngestConfig
	ingestCancel context.CancelFunc
	ingestState  *ingestWorkerState
}

func New(dbPath, nomadAddr string, ingestCfg IngestConfig, settingsStore appconfig.Store) (*Server, error) {
	st, err := store.New(dbPath)
	if err != nil {
		return nil, err
	}
	if ingestCfg.ResetOnStart {
		if err := st.Clear(); err != nil {
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
		store:            st,
		nomad:            nc,
		mux:              http.NewServeMux(),
		settingsStore:    settingsStore,
		ingestServices:   append([]string(nil), ingestCfg.Services...),
		priorityServices: append([]string(nil), ingestCfg.PriorityServices...),
		ingestCfg:        ingestCfg,
	}

	s.routes()
	if ingestCfg.Enabled {
		s.startIngester(ingestCfg)
	}

	return s, nil
}

func (s *Server) NomadAddr() string {
	return s.nomad.Address()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /htmx.min.js", s.handleHTMX)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/jobs", s.handleJobs)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("POST /api/settings", s.handleSaveSettings)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/histogram", s.handleHistogram)
	s.mux.HandleFunc("GET /api/query-suggestions", s.handleQuerySuggestions)
	s.mux.HandleFunc("POST /api/fetch-selected", s.handleFetchSelected)
	s.mux.HandleFunc("GET /api/stream-selected", s.handleStreamSelected)
	s.mux.HandleFunc("POST /api/clear", s.handleClear)
}

// ListenAndServe serves until ctx is cancelled, then shuts down the
// HTTP server, stops the ingester, and closes the store.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:    addr,
		Handler: guardLoopback(addr, s.mux),
		// Derive request contexts from ctx so long-lived SSE handlers
		// exit promptly on shutdown instead of holding Shutdown open.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

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
		fmt.Printf("warning: http shutdown: %v\n", err)
		if err := httpServer.Close(); err != nil {
			fmt.Printf("warning: http close: %v\n", err)
		}
	}
	return s.Close()
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

func (s *Server) Close() error {
	s.ingestMu.Lock()
	cancel := s.ingestCancel
	s.ingestCancel = nil
	s.ingestMu.Unlock()

	if cancel != nil {
		cancel()
	}

	return s.store.Close()
}

func (s *Server) currentIngestServices() []string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return append([]string{}, s.ingestServices...)
}

func (s *Server) currentPriorityServices() []string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return append([]string{}, s.priorityServices...)
}

func (s *Server) updateIngestServices(services []string) error {
	services = cleanServiceList(services)
	if err := s.settingsStore.Save(appconfig.Settings{IngestServices: services}); err != nil {
		return err
	}

	s.settingsMu.Lock()
	s.ingestServices = append([]string(nil), services...)
	s.settingsMu.Unlock()

	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()

	if s.ingestCancel != nil {
		s.ingestCancel()
		s.ingestCancel = nil
	}

	cfg := s.ingestCfg
	cfg.Services = append([]string(nil), services...)
	s.ingestCfg = cfg
	if cfg.Enabled {
		s.startIngester(cfg)
	}
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
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(web.IndexHTML); err != nil {
		fmt.Printf("warning: write index: %v\n", err)
	}
}

func (s *Server) handleHTMX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/javascript")
	if _, err := w.Write(web.HTMXJS); err != nil {
		fmt.Printf("warning: write htmx: %v\n", err)
	}
}
