package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/server"
)

var version = "dev"

func main() {
	if wantsVersion(os.Args[1:]) {
		fmt.Printf("nomadl %s\n", version)
		return
	}

	configDir, err := appconfig.DefaultDir()
	if err != nil {
		log.Fatalf("failed to resolve config dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		log.Fatalf("failed to create config dir: %v", err)
	}

	settingsStore := appconfig.NewStore(configDir)
	settings, err := settingsStore.Load()
	if err != nil {
		log.Fatalf("failed to load settings: %v", err)
	}

	addr := flag.String("addr", "127.0.0.1:7788", "address to listen on")
	nomadAddr := flag.String("nomad-addr", "", "nomad API address (defaults to NOMAD_ADDR env)")
	dbPath := flag.String("db", filepath.Join(configDir, "nomadl.db"), "path to SQLite database")
	ingest := flag.Bool("ingest", true, "continuously ingest Nomad logs into SQLite")
	resetOnStart := flag.Bool("reset-on-start", true, "clear stored logs before starting")
	backfillBytes := flag.Int64("backfill-bytes", 256<<10, "bytes to backfill per task stream on startup")
	backfillWorkers := flag.Int("backfill-workers", 2, "maximum concurrent log backfills")
	discoverInterval := flag.Duration("discover-interval", 15*time.Second, "how often to discover new allocations")
	ingestServices := flag.String("ingest-services", "", "comma-separated services to ingest (default: all running services)")
	ingestStdout := flag.Bool("ingest-stdout", false, "also ingest stdout; stderr is always ingested")
	maxStreams := flag.Int("max-streams", 16, "maximum task log streams to ingest concurrently (0 = unlimited, can hit Nomad connection limits)")
	priorityServices := flag.String("priority-services", "iam,idp,idp-hydra", "comma-separated services to ingest first")
	streamStartDelay := flag.Duration("stream-start-delay", 250*time.Millisecond, "delay between starting live log streams")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Printf("nomadl %s\n", version)
		return
	}
	providedFlags := providedFlagSet()

	ingestCfg := server.DefaultIngestConfig()
	ingestCfg.Enabled = *ingest
	ingestCfg.ResetOnStart = *resetOnStart
	ingestCfg.BackfillBytes = *backfillBytes
	ingestCfg.BackfillWorkers = *backfillWorkers
	ingestCfg.DiscoverInterval = *discoverInterval
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

	srv, err := server.New(*dbPath, *nomadAddr, ingestCfg, settingsStore)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	fmt.Printf("nomadl running at http://%s\n", *addr)
	fmt.Printf("nomad API: %s\n", srv.NomadAddr())
	fmt.Printf("config dir: %s\n", configDir)
	fmt.Printf("database: %s\n", *dbPath)
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
		fmt.Printf("ingesting logs: backfill=%d bytes backfill_workers=%d discover_interval=%s ingest_services=%s max_streams=%s priority_services=%s streams=%s stream_start_delay=%s\n", ingestCfg.BackfillBytes, ingestCfg.BackfillWorkers, ingestCfg.DiscoverInterval, servicesLabel, maxStreamsLabel, priorityLabel, strings.Join(ingestCfg.Streams, ","), ingestCfg.StreamStartDelay)
	}

	if err := srv.ListenAndServe(*addr); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func providedFlagSet() map[string]bool {
	provided := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		provided[f.Name] = true
	})
	return provided
}

func wantsVersion(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--":
			return false
		case "-version", "--version":
			return true
		}
	}
	return false
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
