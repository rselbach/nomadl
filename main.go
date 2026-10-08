package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/server"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	configDir, err := appconfig.DefaultDir()
	if err != nil {
		fatal("resolve config dir", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		fatal("create config dir", err)
	}

	settingsStore := appconfig.NewStore(configDir)
	settings, err := settingsStore.Load()
	if err != nil {
		fatal("load settings", err)
	}

	addr := flag.String("addr", "127.0.0.1:7788", "address to listen on")
	nomadAddr := flag.String("nomad-addr", "", "nomad API address (defaults to NOMAD_ADDR env)")
	dbPath := flag.String("db", filepath.Join(configDir, "nomadl.db"), "path to SQLite database")
	ingest := flag.Bool("ingest", true, "continuously ingest Nomad logs into SQLite")
	resetOnStart := flag.Bool("reset-on-start", true, "clear stored logs before starting")
	backfillBytes := flag.Int64("backfill-bytes", 256<<10, "bytes of recent log to read when a task stream starts")
	discoverInterval := flag.Duration("discover-interval", 15*time.Second, "how often to discover new allocations")
	ingestServices := flag.String("ingest-services", "", "comma-separated services to ingest (default: all running services)")
	ingestStdout := flag.Bool("ingest-stdout", false, "also ingest stdout; stderr is always ingested")
	openBrowser := flag.Bool("open", true, "open the UI in the default browser on startup")
	maxRows := flag.Int("max-rows", 200000, "maximum stored log rows; oldest are pruned (0 = unlimited)")
	maxStreams := flag.Int("max-streams", 64, "maximum task log streams to ingest concurrently (0 = unlimited, can hit Nomad connection limits)")
	priorityServices := flag.String("priority-services", "", "comma-separated services to ingest first")
	streamStartDelay := flag.Duration("stream-start-delay", 250*time.Millisecond, "delay between starting live log streams")
	flag.Parse()
	providedFlags := providedFlagSet()

	ingestCfg := server.DefaultIngestConfig()
	ingestCfg.Enabled = *ingest
	ingestCfg.ResetOnStart = *resetOnStart
	ingestCfg.BackfillBytes = *backfillBytes
	ingestCfg.DiscoverInterval = *discoverInterval
	ingestCfg.MaxRows = *maxRows
	ingestCfg.MaxStreams = *maxStreams
	ingestCfg.PriorityServices = splitCSV(*priorityServices)
	ingestCfg.Services = cleanStrings(settings.IngestServices)
	if providedFlags["ingest-services"] {
		ingestCfg.Services = splitCSV(*ingestServices)
	}
	ingestCfg.Streams = []string{"stderr"}
	if *ingestStdout {
		ingestCfg.Streams = append(ingestCfg.Streams, "stdout")
	}
	ingestCfg.StreamStartDelay = *streamStartDelay

	// An explicitly chosen address must not silently move; only the
	// default port walks forward when it is already taken.
	ln, err := listenWithFallback(*addr, !providedFlags["addr"])
	if err != nil {
		fatal("listen", err)
	}

	srv, err := server.New(*dbPath, *nomadAddr, ingestCfg, settingsStore)
	if err != nil {
		fatal("create server", err)
	}

	uiAddr := uiAddress(ln.Addr().String())
	slog.Info("nomadl running", "url", "http://"+uiAddr, "nomad", srv.NomadAddr(), "config_dir", configDir, "database", *dbPath)
	if ingestCfg.Enabled {
		maxStreamsLabel := "unlimited"
		if ingestCfg.MaxStreams > 0 {
			maxStreamsLabel = fmt.Sprintf("%d", ingestCfg.MaxStreams)
		}
		priorityLabel := "none"
		if len(ingestCfg.PriorityServices) > 0 {
			priorityLabel = strings.Join(ingestCfg.PriorityServices, ",")
		}
		servicesLabel := "all"
		if len(ingestCfg.Services) > 0 {
			servicesLabel = strings.Join(ingestCfg.Services, ",")
		}
		slog.Info("ingesting logs",
			"backfill_bytes", ingestCfg.BackfillBytes,
			"discover_interval", ingestCfg.DiscoverInterval,
			"services", servicesLabel,
			"max_streams", maxStreamsLabel,
			"priority_services", priorityLabel,
			"streams", strings.Join(ingestCfg.Streams, ","),
			"stream_start_delay", ingestCfg.StreamStartDelay)
	}

	if *openBrowser {
		go openBrowserWhenReady(uiAddr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx, ln); err != nil {
		fatal("serve", err)
	}
}

// fatal logs a startup or serving failure and exits.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}

// listenWithFallback binds addr. When the port is already in use and
// fallback is allowed, it walks up one port at a time until a free one
// is found.
func listenWithFallback(addr string, allowFallback bool) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil || !allowFallback || !errors.Is(err, syscall.EADDRINUSE) {
		return ln, err
	}

	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, err
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil {
		return nil, err
	}

	for candidate := port + 1; candidate <= port+100 && candidate <= 65535; candidate++ {
		next := net.JoinHostPort(host, strconv.Itoa(candidate))
		nextLn, nextErr := net.Listen("tcp", next)
		if nextErr == nil {
			slog.Info("default port in use; using the next free one", "port", port, "addr", next)
			return nextLn, nil
		}
		if !errors.Is(nextErr, syscall.EADDRINUSE) {
			return nil, nextErr
		}
	}
	return nil, fmt.Errorf("no free port found above %d: %w", port, err)
}

// uiAddress turns the listen address into one a browser can reach,
// substituting a loopback host for wildcard binds.
func uiAddress(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// openBrowserWhenReady waits for the server to accept connections,
// then opens the UI in the default browser.
func openBrowserWhenReady(uiAddr string) {
	for range 20 {
		conn, err := net.DialTimeout("tcp", uiAddr, 250*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				slog.Warn("close readiness probe", "err", err)
			}
			if err := openInBrowser("http://" + uiAddr); err != nil {
				slog.Warn("open browser", "err", err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Warn("server not reachable; not opening browser", "addr", uiAddr)
}

func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", cmd.Path, err)
	}
	return nil
}

func providedFlagSet() map[string]bool {
	provided := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		provided[f.Name] = true
	})
	return provided
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	return cleanStrings(parts)
}

func cleanStrings(parts []string) []string {
	values := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		values = append(values, part)
	}
	return values
}
