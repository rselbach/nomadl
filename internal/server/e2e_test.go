package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/rselbach/nomadl/internal/store"
)

// TestE2EIngestSurvivesDroppedConnections runs a real Nomad dev agent and
// a job that prints a sequence number every 10ms, cuts every connection
// between nomadl and Nomad three times, and checks that every printed
// number was stored exactly once. It needs the nomad binary and is
// opt-in: run it with NOMADL_E2E=1.
func TestE2EIngestSurvivesDroppedConnections(t *testing.T) {
	if os.Getenv("NOMADL_E2E") != "1" {
		t.Skip("set NOMADL_E2E=1 to run against a real nomad agent -dev")
	}
	nomadBin, err := exec.LookPath("nomad")
	if err != nil {
		t.Skip("nomad binary not found on PATH")
	}

	// nomadl reaches Nomad only through the proxy: the agent advertises
	// the proxy as its HTTP address, so log streams that go straight to
	// the node pass through it too.
	agentPort := freePort(t)
	proxy := newDropProxy(t, "127.0.0.1:"+strconv.Itoa(agentPort))
	startDevAgent(t, nomadBin, agentPort, proxy.port())

	client, err := api.NewClient(&api.Config{Address: "http://" + proxy.addr()})
	if err != nil {
		t.Fatalf("nomad client: %v", err)
	}
	waitFor(t, "a ready node with raw_exec", func() bool {
		nodes, _, err := client.Nodes().List(nil)
		if err != nil || len(nodes) == 0 || nodes[0].Status != "ready" {
			return false
		}
		driver := nodes[0].Drivers["raw_exec"]
		return driver != nil && driver.Healthy
	})

	const jobID = "greendale-e2e"
	registerCounterJob(t, client, jobID)
	waitFor(t, "the counter allocation to run", func() bool {
		allocs, _, err := client.Jobs().Allocations(jobID, false, nil)
		return err == nil && len(allocs) > 0 && allocs[0].ClientStatus == "running"
	})

	srv := newIngestTestServer(t, "http://"+proxy.addr(), func(cfg *IngestConfig) {
		cfg.DiscoverInterval = 500 * time.Millisecond
	})
	waitFor(t, "first lines stored", func() bool {
		count, err := srv.store.Count(t.Context())
		return err == nil && count > 50
	})

	for range 3 {
		if dropped := proxy.dropAll(); dropped == 0 {
			t.Fatal("no open connections to drop; the log stream isn't going through the proxy")
		}
		time.Sleep(1500 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)

	entries, err := srv.store.Search(t.Context(), store.SearchFilters{Query: "service:" + jobID, Limit: 1_000_000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	seqs := make([]int, 0, len(entries))
	for _, entry := range entries {
		n, err := strconv.Atoi(strings.TrimPrefix(entry.Message, "seq "))
		if err != nil {
			t.Fatalf("unexpected line %q", entry.Message)
		}
		seqs = append(seqs, n)
	}
	slices.Sort(seqs)
	for i := 1; i < len(seqs); i++ {
		switch {
		case seqs[i] == seqs[i-1]:
			t.Fatalf("seq %d stored twice", seqs[i])
		case seqs[i] != seqs[i-1]+1:
			t.Fatalf("seq %d to %d missing (%d lines stored)", seqs[i-1]+1, seqs[i]-1, len(seqs))
		}
	}
	t.Logf("stored seq %d to %d with no gaps across 3 dropped connection sets", seqs[0], seqs[len(seqs)-1])
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

func startDevAgent(t *testing.T, nomadBin string, httpPort, advertisePort int) {
	t.Helper()
	dir := t.TempDir()
	config := fmt.Sprintf(`
bind_addr = "127.0.0.1"
ports {
  http = %d
  rpc  = %d
  serf = %d
}
advertise {
  http = "127.0.0.1:%d"
}
client {
  cpu_total_compute = 4000
}
plugin "raw_exec" {
  config {
    enabled = true
  }
}
`, httpPort, freePort(t), freePort(t), advertisePort)
	configPath := filepath.Join(dir, "agent.hcl")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
	logFile, err := os.Create(filepath.Join(dir, "agent.log"))
	if err != nil {
		t.Fatalf("create agent log: %v", err)
	}

	cmd := exec.Command(nomadBin, "agent", "-dev", "-config", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nomad agent: %v", err)
	}
	t.Cleanup(func() {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Errorf("stop nomad agent: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			if err := cmd.Process.Kill(); err != nil {
				t.Errorf("kill nomad agent: %v", err)
			}
		}
		if err := logFile.Close(); err != nil {
			t.Errorf("close agent log: %v", err)
		}
		if t.Failed() {
			if out, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("agent log tail:\n%s", tail(string(out), 40))
			}
		}
	})
}

func registerCounterJob(t *testing.T, client *api.Client, jobID string) {
	t.Helper()
	job := api.NewServiceJob(jobID, jobID, "global", 50)
	job.Datacenters = []string{"dc1"}
	task := api.NewTask("server", "raw_exec").
		SetConfig("command", "/bin/sh").
		SetConfig("args", []string{"-c", `i=0; while true; do i=$((i+1)); echo "seq $i" >&2; sleep 0.01; done`}).
		Require(&api.Resources{CPU: new(10), MemoryMB: new(32)})
	job.AddTaskGroup(api.NewTaskGroup("web", 1).AddTask(task))
	if _, _, err := client.Jobs().Register(job, nil); err != nil {
		t.Fatalf("register job: %v", err)
	}
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(parts[max(0, len(parts)-lines):], "\n")
}

// dropProxy forwards TCP connections to a target and can cut all of them
// at once, to simulate a network blip between nomadl and Nomad.
type dropProxy struct {
	t      *testing.T
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newDropProxy(t *testing.T, target string) *dropProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &dropProxy{t: t, ln: ln, target: target, conns: make(map[net.Conn]struct{})}
	go p.serve()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close proxy: %v", err)
		}
		p.dropAll()
	})
	return p
}

func (p *dropProxy) addr() string { return p.ln.Addr().String() }

func (p *dropProxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *dropProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.forward(conn)
	}
}

func (p *dropProxy) forward(down net.Conn) {
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		p.closeConn(down)
		return
	}
	p.track(down, up)
	done := make(chan struct{})
	go func() {
		p.copy(up, down)
		close(done)
	}()
	p.copy(down, up)
	<-done
	p.closeConn(down)
	p.closeConn(up)
}

// copy forwards one direction. Network errors are how a forward ends when
// the proxy cuts a connection or the other side closes it.
func (p *dropProxy) copy(dst, src net.Conn) {
	var opErr *net.OpError
	if _, err := io.Copy(dst, src); err != nil && !errors.As(err, &opErr) {
		p.t.Logf("proxy copy: %v", err)
	}
	p.closeConn(dst)
}

func (p *dropProxy) track(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range conns {
		p.conns[c] = struct{}{}
	}
}

func (p *dropProxy) closeConn(c net.Conn) {
	p.mu.Lock()
	_, tracked := p.conns[c]
	delete(p.conns, c)
	p.mu.Unlock()
	if err := c.Close(); err != nil && tracked && !errors.Is(err, net.ErrClosed) {
		p.t.Logf("proxy close: %v", err)
	}
}

// dropAll closes every open connection and reports how many there were.
func (p *dropProxy) dropAll() int {
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		p.closeConn(c)
	}
	return len(conns)
}
