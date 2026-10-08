package nomad

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/rselbach/nomadl/internal/store"
)

// Client wraps the Nomad API client with the calls nomadl needs.
type Client struct {
	client *api.Client
}

// Task identifies one running task whose logs can be followed.
type Task struct {
	Service   string
	AllocID   string
	NodeID    string
	Namespace string
	Name      string
}

// NewClient returns a client for the Nomad API at addr, or the address
// from the standard Nomad environment when addr is empty.
func NewClient(addr string) (*Client, error) {
	config := api.DefaultConfig()
	if addr != "" {
		config.Address = addr
	}

	client, err := api.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create nomad client: %w", err)
	}

	return &Client{client: client}, nil
}

// Address returns the Nomad API address in use.
func (c *Client) Address() string {
	return c.client.Address()
}

// RunningTasks lists the running tasks of every running allocation with a
// single API call, ordered by service, allocation, and task name.
func (c *Client) RunningTasks(ctx context.Context) ([]Task, error) {
	q := (&api.QueryOptions{Filter: `ClientStatus == "running"`}).WithContext(ctx)
	stubs, _, err := c.client.Allocations().List(q)
	if err != nil {
		return nil, fmt.Errorf("list running allocations: %w", err)
	}

	var tasks []Task
	for _, stub := range stubs {
		for name, state := range stub.TaskStates {
			if state == nil || state.State != "running" {
				continue
			}
			tasks = append(tasks, Task{
				Service:   stub.JobID,
				AllocID:   stub.ID,
				NodeID:    stub.NodeID,
				Namespace: stub.Namespace,
				Name:      name,
			})
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.AllocID != b.AllocID {
			return a.AllocID < b.AllocID
		}
		return a.Name < b.Name
	})
	return tasks, nil
}

// Follow streams one task log, starting backBytes before its current end,
// and keeps following new output. Complete lines reach emit in file order.
// It returns ctx's error once ctx is cancelled, or an error when the
// stream fails or ends so the caller can reconnect. A stream on a stopped
// task never ends on its own; cancel ctx to stop it.
func (c *Client) Follow(ctx context.Context, task Task, stream string, backBytes int64, emit func(store.LogEntry)) error {
	alloc := &api.Allocation{ID: task.AllocID, NodeID: task.NodeID, Namespace: task.Namespace}
	q := (&api.QueryOptions{Namespace: task.Namespace}).WithContext(ctx)
	// ctx.Done() doubles as the api's cancel channel, so its reader stops
	// between frames as well as when the request itself is cancelled.
	frames, errCh := c.client.AllocFS().Logs(alloc, true, task.Name, stream, "end", backBytes, ctx.Done(), q)

	var lines frameLines
	var guess timeGuesser
	firstFrame := true
	dropPartial := false
	emitLine := func(line, file string, offset int64) {
		if dropPartial {
			dropPartial = false
			return
		}
		entry := parseLogLine(line, task.Service, task.AllocID, task.Name, stream)
		entry.LineRef = lineRef(file, offset)
		guess.stamp(&entry, time.Now())
		emit(entry)
	}

	// Keep receiving until the api's reader signals it is done, even after
	// ctx is cancelled: returning early would strand it on a channel send.
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				// The unfinished tail is not flushed; the reconnect re-reads
				// it whole.
				return fmt.Errorf("%s log stream ended", stream)
			}
			if ctx.Err() != nil || len(frame.Data) == 0 {
				continue
			}
			if firstFrame {
				firstFrame = false
				// Starting before the end usually lands mid-line; drop that
				// fragment rather than store a line whose content depends
				// on where the read began.
				dropPartial = frame.Offset > int64(len(frame.Data))
			}
			lines.add(frame, emitLine)
		case err := <-errCh:
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("%s logs: %w", stream, err)
		}
	}
}

// frameLines splits streamed nomad log frames into complete lines while
// tracking each line's absolute byte offset within its source file, so
// entries can carry a position reference that is stable across refetches.
// A frame's Offset is the file offset after its data (see the nomad api
// FrameReader, which resumes from frame.Offset).
type frameLines struct {
	file    string
	start   int64
	partial []byte
}

func (fl *frameLines) add(frame *api.StreamFrame, emit func(line, file string, offset int64)) {
	if len(frame.Data) == 0 {
		return
	}
	base := frame.Offset - int64(len(frame.Data))
	if frame.File != fl.file {
		// Log rotation: the partial tail of the previous file is complete.
		fl.flush(emit)
		fl.file = frame.File
		fl.start = base
	} else if len(fl.partial) == 0 {
		fl.start = base
	}

	fl.partial = append(fl.partial, frame.Data...)
	for {
		idx := bytes.IndexByte(fl.partial, '\n')
		if idx < 0 {
			return
		}
		if idx > 0 {
			emit(string(fl.partial[:idx]), fl.file, fl.start)
		}
		fl.partial = fl.partial[idx+1:]
		fl.start += int64(idx + 1)
	}
}

// flush emits the pending partial line, if any.
func (fl *frameLines) flush(emit func(line, file string, offset int64)) {
	if len(fl.partial) == 0 {
		return
	}
	line := string(fl.partial)
	fl.partial = nil
	emit(line, fl.file, fl.start)
}

// continuationWindow is how soon after a timestamped line a line without
// a timestamp must arrive to share its time.
const continuationWindow = time.Second

// timeGuesser fills in timestamps for lines that carry none. A line that
// arrives right after a timestamped line, as continuation lines (stack
// traces) and backfilled history do, takes that line's time; any other
// line takes its arrival time. Only real timestamps are inherited, so a
// steady stream of untimestamped lines still advances with the clock.
type timeGuesser struct {
	lastReal        time.Time
	lastRealArrival time.Time
}

func (g *timeGuesser) stamp(entry *store.LogEntry, arrival time.Time) {
	if !entry.Timestamp.IsZero() {
		g.lastReal = entry.Timestamp
		g.lastRealArrival = arrival
		return
	}
	entry.TimeInferred = true
	entry.Timestamp = arrival
	if !g.lastReal.IsZero() && arrival.Sub(g.lastRealArrival) < continuationWindow {
		entry.Timestamp = g.lastReal
	}
}

func lineRef(file string, offset int64) string {
	if file == "" {
		return ""
	}
	return file + "@" + strconv.FormatInt(offset, 10)
}

// parseLogLine extracts the timestamp, level, and message from a JSON,
// bracketed, or logfmt line. Timestamp stays zero when the line has none.
func parseLogLine(line, job, allocID, task, stream string) store.LogEntry {
	entry := store.LogEntry{
		Job:     job,
		AllocID: allocID,
		Task:    task,
		Stream:  stream,
		Level:   "UNKNOWN",
		Message: line,
		Raw:     line,
	}

	var j map[string]any
	if err := json.Unmarshal([]byte(line), &j); err == nil {
		entry.Timestamp = extractTimestamp(j)
		entry.Level = extractLevel(j)
		entry.Message = extractMessage(j)
		if entry.Message == "" {
			entry.Message = line
		}
		return entry
	}

	if ts, level, msg, ok := parseTextLogLine(line); ok {
		entry.Timestamp = ts
		entry.Level = level
		entry.Message = msg
		return entry
	}

	if ts, level, msg, ok := parseLogfmtLine(line); ok {
		entry.Timestamp = ts
		entry.Level = level
		entry.Message = msg
		return entry
	}

	return entry
}

var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05.000000Z",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05",
	time.UnixDate,
}

func extractTimestamp(j map[string]any) time.Time {
	for _, key := range []string{"timestamp", "time", "@timestamp", "ts", "datetime", "@time"} {
		switch v := j[key].(type) {
		case string:
			if t, ok := parseTimestampValue(v); ok {
				return t
			}
		case float64:
			if t, ok := timeFromEpoch(v); ok {
				return t
			}
		}
	}
	return time.Time{}
}

func parseTimestampValue(value string) (time.Time, bool) {
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	if epoch, err := strconv.ParseFloat(value, 64); err == nil {
		return timeFromEpoch(epoch)
	}
	return time.Time{}, false
}

// timeFromEpoch interprets a numeric timestamp, inferring the unit
// (seconds, milliseconds, microseconds, or nanoseconds) from its
// magnitude.
func timeFromEpoch(v float64) (time.Time, bool) {
	if v <= 0 {
		return time.Time{}, false
	}
	switch {
	case v >= 1e17: // nanoseconds
		return time.Unix(0, int64(v)), true
	case v >= 1e14: // microseconds
		return time.Unix(0, int64(v)*int64(time.Microsecond)), true
	case v >= 1e11: // milliseconds
		return time.Unix(0, int64(v)*int64(time.Millisecond)), true
	default: // seconds, possibly fractional
		sec, frac := math.Modf(v)
		return time.Unix(int64(sec), int64(frac*float64(time.Second))), true
	}
}

func extractLevel(j map[string]any) string {
	for _, key := range []string{"level", "severity", "lvl", "loglevel", "@level", "@severity"} {
		if v, ok := j[key]; ok {
			if s, ok := v.(string); ok {
				return strings.ToUpper(s)
			}
		}
	}
	return "UNKNOWN"
}

func extractMessage(j map[string]any) string {
	for _, key := range []string{"message", "msg", "log", "text", "@message", "@msg"} {
		if v, ok := j[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func parseTextLogLine(line string) (time.Time, string, string, bool) {
	idx := strings.Index(line, "[")
	if idx < 0 {
		return time.Time{}, "", "", false
	}

	end := strings.Index(line[idx:], "]")
	if end < 0 {
		return time.Time{}, "", "", false
	}

	levelStr := line[idx+1 : idx+end]
	if !isValidLevel(levelStr) {
		return time.Time{}, "", "", false
	}

	msg := strings.TrimSpace(line[idx+end+1:])
	tsPart := strings.TrimSpace(line[:idx])

	for _, layout := range timestampLayouts {
		if ts, err := time.Parse(layout, tsPart); err == nil {
			return ts, strings.ToUpper(levelStr), msg, true
		}
	}

	return time.Time{}, "", "", false
}

// parseLogfmtLine handles logfmt output (key=value pairs), the format
// used by much of the HashiCorp ecosystem. To avoid false positives on
// lines that merely contain an equals sign, it only claims a line that
// carries a recognized level or both a timestamp and a message.
func parseLogfmtLine(line string) (time.Time, string, string, bool) {
	pairs := logfmtPairs(line)
	if len(pairs) == 0 {
		return time.Time{}, "", "", false
	}

	level := ""
	for _, key := range []string{"level", "lvl", "severity"} {
		if v, ok := pairs[key]; ok && isValidLevel(v) {
			level = strings.ToUpper(v)
			break
		}
	}

	msg, msgOK := pairs["msg"]
	if !msgOK {
		msg, msgOK = pairs["message"]
	}

	var ts time.Time
	tsOK := false
	for _, key := range []string{"ts", "time", "timestamp", "t"} {
		v, ok := pairs[key]
		if !ok {
			continue
		}
		if parsed, ok := parseTimestampValue(v); ok {
			ts = parsed
			tsOK = true
			break
		}
	}

	if level == "" && (!msgOK || !tsOK) {
		return time.Time{}, "", "", false
	}
	if level == "" {
		level = "UNKNOWN"
	}
	if msg == "" {
		msg = line
	}
	return ts, level, msg, true
}

func logfmtPairs(line string) map[string]string {
	pairs := make(map[string]string)
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}

		keyStart := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' {
			continue
		}
		key := line[keyStart:i]
		i++

		var value string
		if i < len(line) && line[i] == '"' {
			i++
			var b strings.Builder
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				}
				b.WriteByte(line[i])
				i++
			}
			i++
			value = b.String()
		} else {
			valueStart := i
			for i < len(line) && line[i] != ' ' {
				i++
			}
			value = line[valueStart:i]
		}
		if key != "" {
			pairs[key] = value
		}
	}
	return pairs
}

func isValidLevel(s string) bool {
	switch strings.ToUpper(s) {
	case "TRACE", "DEBUG", "INFO", "NOTICE", "WARN", "WARNING",
		"ERROR", "ERR", "CRITICAL", "CRIT", "ALERT", "EMERGENCY", "FATAL", "PANIC":
		return true
	default:
		return false
	}
}
