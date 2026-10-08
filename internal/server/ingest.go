package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rselbach/nomadl/internal/nomad"
	"github.com/rselbach/nomadl/internal/store"
)

// IngestConfig controls log ingestion.
type IngestConfig struct {
	Enabled          bool
	ResetOnStart     bool
	BackfillBytes    int64
	DiscoverInterval time.Duration
	MaxRows          int
	MaxStreams       int
	PriorityServices []string
	Services         []string
	Streams          []string
	StreamStartDelay time.Duration
}

// DefaultIngestConfig returns the ingestion defaults used by the CLI.
func DefaultIngestConfig() IngestConfig {
	return IngestConfig{
		Enabled:          true,
		ResetOnStart:     true,
		BackfillBytes:    256 << 10,
		DiscoverInterval: 15 * time.Second,
		MaxRows:          200_000,
		MaxStreams:       16,
		PriorityServices: nil,
		Services:         nil,
		Streams:          []string{"stderr"},
		StreamStartDelay: 250 * time.Millisecond,
	}
}

const (
	// nomadCallTimeout bounds each discovery call so a hung Nomad API
	// can't stall the loop.
	nomadCallTimeout = 10 * time.Second
	// minReconnectOverlap is how much log a reconnecting stream re-reads
	// at minimum, so output written while it was down isn't lost; line
	// dedupe drops what was already stored.
	minReconnectOverlap = 64 << 10
	maxReconnectBackoff = 30 * time.Second
)

// target is one log stream of one running task.
type target struct {
	task   nomad.Task
	stream string
}

func (t target) key() string {
	return t.task.AllocID + "/" + t.task.Name + "/" + t.stream
}

func (t target) label() string {
	return t.task.Service + "/" + t.task.Name + " " + t.stream + " (" + shortID(t.task.AllocID) + ")"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ingester discovers running Nomad tasks and keeps one follower per log
// stream. The discovery loop is the only place followers start or stop:
// a follower reconnects on its own until discovery cancels it because
// its task stopped or its service left the allowlist.
type ingester struct {
	nomad *nomad.Client
	store *store.Store
	cfg   IngestConfig

	kick      chan struct{}
	firstDone chan struct{}
	firstOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	followWG  sync.WaitGroup

	mu        sync.Mutex
	services  []string
	running   []string
	lastRun   time.Time
	lastErr   error
	followers map[string]*follower
}

type follower struct {
	target target
	cancel context.CancelFunc
}

// ingestStatus is a snapshot of discovery and ingestion state.
type ingestStatus struct {
	enabled       bool
	streams       []string
	maxStreams    int
	services      []string
	running       []string
	lastDiscovery time.Time
	nomadErr      error
	active        []string
}

func newIngester(nc *nomad.Client, st *store.Store, cfg IngestConfig) *ingester {
	if cfg.DiscoverInterval <= 0 {
		cfg.DiscoverInterval = 15 * time.Second
	}
	if cfg.BackfillBytes < 0 {
		cfg.BackfillBytes = 0
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
	return &ingester{
		nomad:     nc,
		store:     st,
		cfg:       cfg,
		kick:      make(chan struct{}, 1),
		firstDone: make(chan struct{}),
		done:      make(chan struct{}),
		services:  cleanServiceList(cfg.Services),
		followers: make(map[string]*follower),
	}
}

// start runs discovery in the background until stop is called. Discovery
// runs even when ingestion is disabled so the UI can list services.
func (in *ingester) start() {
	ctx, cancel := context.WithCancel(context.Background())
	in.cancel = cancel
	go func() {
		defer close(in.done)
		in.run(ctx)
		in.followWG.Wait()
	}()
}

// stop cancels discovery and every follower and waits for them to exit.
func (in *ingester) stop() {
	in.cancel()
	<-in.done
}

func (in *ingester) run(ctx context.Context) {
	ticker := time.NewTicker(in.cfg.DiscoverInterval)
	defer ticker.Stop()

	for {
		in.discover(ctx)
		in.prune()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-in.kick:
		}
	}
}

func (in *ingester) discover(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, nomadCallTimeout)
	tasks, err := in.nomad.RunningTasks(callCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	defer in.firstOnce.Do(func() { close(in.firstDone) })

	in.lastRun = time.Now()
	in.lastErr = err
	if err != nil {
		// Existing followers stay: they reconnect on their own, and a
		// failed listing says nothing about whether their tasks stopped.
		fmt.Printf("warning: discover nomad tasks: %v\n", err)
		return
	}
	in.running = serviceNames(tasks)
	if in.cfg.Enabled {
		in.reconcileLocked(ctx, in.targetsLocked(tasks))
	}
}

// reconcileLocked cancels followers whose target is gone and starts
// followers for new targets, in priority order, up to the stream cap.
func (in *ingester) reconcileLocked(ctx context.Context, targets []target) {
	wanted := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		wanted[t.key()] = struct{}{}
	}
	for key, f := range in.followers {
		if _, ok := wanted[key]; !ok {
			f.cancel()
			delete(in.followers, key)
		}
	}

	started := 0
	for _, t := range targets {
		if _, ok := in.followers[t.key()]; ok {
			continue
		}
		if in.cfg.MaxStreams > 0 && len(in.followers) >= in.cfg.MaxStreams {
			break
		}
		followCtx, cancel := context.WithCancel(ctx)
		in.followers[t.key()] = &follower{target: t, cancel: cancel}
		// Stagger connection starts so a large discovery doesn't open
		// every stream at once.
		delay := time.Duration(started) * in.cfg.StreamStartDelay
		started++
		in.followWG.Add(1)
		go func() {
			defer in.followWG.Done()
			in.follow(followCtx, t, delay)
		}()
	}
}

// follow streams one target into the store, reconnecting with backoff
// until ctx is cancelled.
func (in *ingester) follow(ctx context.Context, t target, delay time.Duration) {
	if !sleepOrDone(ctx, delay) {
		return
	}

	backBytes := in.cfg.BackfillBytes
	backoff := time.Second
	for {
		connected := time.Now()
		err := in.nomad.Follow(ctx, t.task, t.stream, backBytes, func(entry store.LogEntry) {
			if err := in.store.InsertLog(entry); err != nil {
				fmt.Printf("warning: store log %s: %v\n", t.label(), err)
			}
		})
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("warning: follow %s: %v; reconnecting in %s\n", t.label(), err, backoff)

		backBytes = max(in.cfg.BackfillBytes, minReconnectOverlap)
		if time.Since(connected) > time.Minute {
			backoff = time.Second
		}
		if !sleepOrDone(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, maxReconnectBackoff)
	}
}

// targetsLocked turns running tasks into log targets for the allowed
// services, priority services first.
func (in *ingester) targetsLocked(tasks []nomad.Task) []target {
	services := filterServices(serviceNames(tasks), in.services)
	services = prioritizeServices(services, in.cfg.PriorityServices)
	rank := make(map[string]int, len(services))
	for i, service := range services {
		rank[service] = i
	}

	var allowed []nomad.Task
	for _, task := range tasks {
		if _, ok := rank[task.Service]; ok {
			allowed = append(allowed, task)
		}
	}
	// tasks arrive sorted by service, allocation, and name; a stable sort
	// by rank keeps that order within each service.
	sort.SliceStable(allowed, func(i, j int) bool {
		return rank[allowed[i].Service] < rank[allowed[j].Service]
	})

	targets := make([]target, 0, len(allowed)*len(in.cfg.Streams))
	for _, task := range allowed {
		for _, stream := range in.cfg.Streams {
			targets = append(targets, target{task: task, stream: stream})
		}
	}
	return targets
}

func (in *ingester) prune() {
	if in.cfg.MaxRows <= 0 {
		return
	}
	deleted, err := in.store.Prune(in.cfg.MaxRows)
	if err != nil {
		fmt.Printf("warning: prune store: %v\n", err)
		return
	}
	if deleted > 0 {
		fmt.Printf("pruned %d old log rows (keeping newest %d)\n", deleted, in.cfg.MaxRows)
	}
}

// waitFirstDiscovery blocks until the first discovery finished or ctx is
// done, so requests right after startup don't see an empty service list.
func (in *ingester) waitFirstDiscovery(ctx context.Context) {
	select {
	case <-in.firstDone:
	case <-ctx.Done():
	}
}

// setServices replaces the ingest allowlist and triggers a discovery so
// followers start and stop right away.
func (in *ingester) setServices(services []string) {
	in.mu.Lock()
	in.services = cleanServiceList(services)
	in.mu.Unlock()

	select {
	case in.kick <- struct{}{}:
	default:
	}
}

// visibleServices returns running services that are ingested, priority
// services first.
func (in *ingester) visibleServices() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	services := filterServices(in.running, in.services)
	return prioritizeServices(services, in.cfg.PriorityServices)
}

func (in *ingester) status() ingestStatus {
	in.mu.Lock()
	defer in.mu.Unlock()

	active := make([]string, 0, len(in.followers))
	for _, f := range in.followers {
		active = append(active, f.target.label())
	}
	sort.Strings(active)
	return ingestStatus{
		enabled:       in.cfg.Enabled,
		streams:       append([]string(nil), in.cfg.Streams...),
		maxStreams:    in.cfg.MaxStreams,
		services:      append([]string(nil), in.services...),
		running:       append([]string(nil), in.running...),
		lastDiscovery: in.lastRun,
		nomadErr:      in.lastErr,
		active:        active,
	}
}

func serviceNames(tasks []nomad.Task) []string {
	var names []string
	seen := make(map[string]struct{})
	for _, task := range tasks {
		if _, ok := seen[task.Service]; ok {
			continue
		}
		seen[task.Service] = struct{}{}
		names = append(names, task.Service)
	}
	sort.Strings(names)
	return names
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
		return ctx.Err() == nil
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
