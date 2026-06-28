package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type IngestConfig struct {
	Enabled          bool
	ResetOnStart     bool
	BackfillBytes    int64
	BackfillWorkers  int
	DiscoverInterval time.Duration
	MaxStreams       int
	PriorityServices []string
	Services         []string
	Streams          []string
	StreamStartDelay time.Duration
}

func DefaultIngestConfig() IngestConfig {
	return IngestConfig{
		Enabled:          true,
		ResetOnStart:     true,
		BackfillBytes:    256 << 10,
		BackfillWorkers:  2,
		DiscoverInterval: 15 * time.Second,
		MaxStreams:       16,
		PriorityServices: []string{"iam", "idp", "idp-hydra"},
		Services:         nil,
		Streams:          []string{"stderr"},
		StreamStartDelay: 250 * time.Millisecond,
	}
}

type ingestWorkerState struct {
	mu        sync.Mutex
	workers   map[string]struct{}
	backfills map[string]struct{}
	backfillQ chan logTarget
}

func (s *Server) startIngester(cfg IngestConfig) {
	if cfg.DiscoverInterval <= 0 {
		cfg.DiscoverInterval = 15 * time.Second
	}
	if cfg.BackfillBytes < 0 {
		cfg.BackfillBytes = 0
	}
	if cfg.BackfillWorkers <= 0 {
		cfg.BackfillWorkers = 1
	}
	if cfg.MaxStreams < 0 {
		cfg.MaxStreams = 0
	}
	if cfg.StreamStartDelay < 0 {
		cfg.StreamStartDelay = 0
	}
	if len(cfg.Streams) == 0 {
		cfg.Streams = []string{"stderr"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ingestCancel = cancel

	backfillQueueSize := cfg.BackfillWorkers * 64
	if backfillQueueSize < 64 {
		backfillQueueSize = 64
	}
	state := &ingestWorkerState{
		workers:   make(map[string]struct{}),
		backfills: make(map[string]struct{}),
		backfillQ: make(chan logTarget, backfillQueueSize),
	}
	for range cfg.BackfillWorkers {
		go s.runBackfillWorker(ctx, cfg.BackfillBytes, cfg.Streams, state)
	}
	go s.runIngester(ctx, cfg, state)
}

func (s *Server) runIngester(ctx context.Context, cfg IngestConfig, state *ingestWorkerState) {
	s.discoverIngestTargets(ctx, cfg, state)

	ticker := time.NewTicker(cfg.DiscoverInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.discoverIngestTargets(ctx, cfg, state)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) discoverIngestTargets(ctx context.Context, cfg IngestConfig, state *ingestWorkerState) {
	jobs, err := s.nomad.ListJobs()
	if err != nil {
		fmt.Printf("warning: ingest discovery jobs: %v\n", err)
		return
	}

	services := make([]string, 0, len(jobs))
	for _, job := range jobs {
		services = append(services, job.ID)
	}
	sort.Strings(services)
	services = filterServices(services, cfg.Services)
	services = prioritizeServices(services, cfg.PriorityServices)

	targets, errs := s.logTargets(services)
	for _, err := range errs {
		fmt.Printf("warning: ingest discovery target: %v\n", err)
	}

	for _, target := range targets {
		s.queueIngestBackfill(ctx, target, cfg.BackfillBytes, state)
	}

	for _, target := range targets {
		select {
		case <-ctx.Done():
			return
		default:
		}

		key := target.allocID + ":" + target.task
		state.mu.Lock()
		if _, ok := state.workers[key]; ok {
			state.mu.Unlock()
			continue
		}
		if cfg.MaxStreams > 0 && len(state.workers) >= cfg.MaxStreams {
			state.mu.Unlock()
			break
		}
		state.workers[key] = struct{}{}
		state.mu.Unlock()

		go func() {
			defer func() {
				state.mu.Lock()
				delete(state.workers, key)
				state.mu.Unlock()
			}()
			s.ingestTarget(ctx, target, cfg.Streams)
		}()

		if !sleepOrDone(ctx, cfg.StreamStartDelay) {
			return
		}
	}
}

func (s *Server) queueIngestBackfill(ctx context.Context, target logTarget, backfillBytes int64, state *ingestWorkerState) {
	if backfillBytes == 0 {
		return
	}

	key := target.allocID + ":" + target.task
	state.mu.Lock()
	if _, ok := state.backfills[key]; ok {
		state.mu.Unlock()
		return
	}
	state.backfills[key] = struct{}{}
	state.mu.Unlock()

	select {
	case state.backfillQ <- target:
	case <-ctx.Done():
		state.mu.Lock()
		delete(state.backfills, key)
		state.mu.Unlock()
	default:
		state.mu.Lock()
		delete(state.backfills, key)
		state.mu.Unlock()
		fmt.Printf("warning: ingest backfill queue full; will retry %s/%s later\n", target.service, target.task)
	}
}

func (s *Server) runBackfillWorker(ctx context.Context, backfillBytes int64, streams []string, state *ingestWorkerState) {
	for {
		select {
		case target := <-state.backfillQ:
			select {
			case <-ctx.Done():
				return
			default:
			}
			s.ingestBackfill(target, backfillBytes, streams, state)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) ingestBackfill(target logTarget, backfillBytes int64, streams []string, state *ingestWorkerState) {
	key := target.allocID + ":" + target.task
	entries, err := s.nomad.FetchLogStreams(target.allocID, target.task, backfillBytes, streams)
	if err != nil {
		state.mu.Lock()
		delete(state.backfills, key)
		state.mu.Unlock()
		fmt.Printf("warning: ingest backfill %s/%s: %v\n", target.service, target.task, err)
		return
	}
	if err := s.store.InsertLogs(entries); err != nil {
		state.mu.Lock()
		delete(state.backfills, key)
		state.mu.Unlock()
		fmt.Printf("warning: ingest store backfill %s/%s: %v\n", target.service, target.task, err)
	}
}

func (s *Server) ingestTarget(ctx context.Context, target logTarget, streams []string) {
	stream, errs := s.nomad.StreamLogStreams(target.allocID, target.task, streams, ctx.Done())

	for {
		select {
		case entry, ok := <-stream:
			if !ok {
				return
			}
			if err := s.store.InsertLog(entry); err != nil {
				fmt.Printf("warning: ingest store log %s/%s: %v\n", target.service, target.task, err)
			}
		case err, ok := <-errs:
			if ok && err != nil {
				fmt.Printf("warning: ingest stream %s/%s: %v\n", target.service, target.task, err)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func prioritizeServices(services, priority []string) []string {
	if len(priority) == 0 {
		return services
	}

	serviceSet := make(map[string]struct{}, len(services))
	for _, service := range services {
		serviceSet[service] = struct{}{}
	}

	ordered := make([]string, 0, len(services))
	seen := make(map[string]struct{}, len(services))
	for _, service := range priority {
		if _, ok := serviceSet[service]; !ok {
			continue
		}
		if _, ok := seen[service]; ok {
			continue
		}
		ordered = append(ordered, service)
		seen[service] = struct{}{}
	}
	for _, service := range services {
		if _, ok := seen[service]; ok {
			continue
		}
		ordered = append(ordered, service)
	}
	return ordered
}

func filterServices(services, allowlist []string) []string {
	if len(allowlist) == 0 {
		return services
	}

	running := make(map[string]struct{}, len(services))
	for _, service := range services {
		running[service] = struct{}{}
	}

	filtered := make([]string, 0, len(services))
	seen := make(map[string]struct{}, len(allowlist))
	for _, service := range allowlist {
		if _, ok := running[service]; !ok {
			continue
		}
		if _, ok := seen[service]; ok {
			continue
		}
		filtered = append(filtered, service)
		seen[service] = struct{}{}
	}
	return filtered
}

func sleepOrDone(ctx context.Context, delay time.Duration) bool {
	if delay == 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
