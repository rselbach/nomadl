package nomad

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/rselbach/nomadl/internal/store"
)

type Client struct {
	client *api.Client
}

type JobInfo struct {
	ID     string
	Name   string
	Type   string
	Status string
}

type AllocInfo struct {
	ID           string
	JobID        string
	Name         string
	NodeName     string
	TaskGroup    string
	ClientStatus string
	Tasks        []TaskInfo
}

type TaskInfo struct {
	Name  string
	State string
}

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

func (c *Client) Address() string {
	return c.client.Address()
}

func (c *Client) ListJobs() ([]JobInfo, error) {
	jobs, _, err := c.client.Jobs().List(nil)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	result := make([]JobInfo, 0, len(jobs))
	for _, j := range jobs {
		if j.Status != "running" {
			continue
		}
		result = append(result, JobInfo{
			ID:     j.ID,
			Name:   j.Name,
			Type:   j.Type,
			Status: j.Status,
		})
	}
	return result, nil
}

func (c *Client) ListAllocations(jobID string) ([]AllocInfo, error) {
	allocs, _, err := c.client.Jobs().Allocations(jobID, false, nil)
	if err != nil {
		return nil, fmt.Errorf("list allocations for job %s: %w", jobID, err)
	}

	var jobTasks map[string][]TaskInfo
	var jobTasksErr error
	result := make([]AllocInfo, 0, len(allocs))
	for _, a := range allocs {
		info := AllocInfo{
			ID:           a.ID,
			JobID:        a.JobID,
			Name:         a.Name,
			NodeName:     a.NodeName,
			TaskGroup:    a.TaskGroup,
			ClientStatus: a.ClientStatus,
		}

		info.Tasks = taskInfosFromStates(a.TaskStates)
		if len(info.Tasks) == 0 {
			alloc, _, err := c.client.Allocations().Info(a.ID, nil)
			if err == nil {
				info.Tasks = taskInfosFromStates(alloc.TaskStates)
				if info.TaskGroup == "" {
					info.TaskGroup = alloc.TaskGroup
				}
			}
		}
		if len(info.Tasks) == 0 {
			if jobTasks == nil && jobTasksErr == nil {
				jobTasks, jobTasksErr = c.tasksByGroup(jobID)
			}
			if jobTasksErr == nil {
				info.Tasks = jobTasks[info.TaskGroup]
			}
		}

		result = append(result, info)
	}
	return result, nil
}

func taskInfosFromStates(states map[string]*api.TaskState) []TaskInfo {
	if len(states) == 0 {
		return nil
	}

	tasks := make([]TaskInfo, 0, len(states))
	for name, state := range states {
		status := "unknown"
		if state != nil && state.State != "" {
			status = state.State
		}
		tasks = append(tasks, TaskInfo{Name: name, State: status})
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].Name < tasks[j].Name
	})
	return tasks
}

func (c *Client) tasksByGroup(jobID string) (map[string][]TaskInfo, error) {
	job, _, err := c.client.Jobs().Info(jobID, nil)
	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", jobID, err)
	}

	tasksByGroup := make(map[string][]TaskInfo, len(job.TaskGroups))
	for _, group := range job.TaskGroups {
		if group == nil || group.Name == nil {
			continue
		}
		groupName := *group.Name
		for _, task := range group.Tasks {
			if task == nil || task.Name == "" {
				continue
			}
			tasksByGroup[groupName] = append(tasksByGroup[groupName], TaskInfo{
				Name:  task.Name,
				State: "configured",
			})
		}
		sort.Slice(tasksByGroup[groupName], func(i, j int) bool {
			return tasksByGroup[groupName][i].Name < tasksByGroup[groupName][j].Name
		})
	}
	return tasksByGroup, nil
}

func (c *Client) FetchLogs(allocID, task string, fetchBytes int64) ([]store.LogEntry, error) {
	return c.FetchLogStreams(allocID, task, fetchBytes, []string{"stdout", "stderr"})
}

func (c *Client) FetchLogStreams(allocID, task string, fetchBytes int64, streams []string) ([]store.LogEntry, error) {
	if len(streams) == 0 {
		return nil, errors.New("at least one log stream is required")
	}

	alloc, _, err := c.client.Allocations().Info(allocID, nil)
	if err != nil {
		return nil, fmt.Errorf("get allocation %s: %w", allocID, err)
	}

	var entries []store.LogEntry
	var streamErrors []error

	for _, stream := range streams {
		parsed, err := c.fetchLogStream(alloc, allocID, task, stream, fetchBytes)
		if err != nil {
			streamErrors = append(streamErrors, err)
			continue
		}
		entries = append(entries, parsed...)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	if len(entries) == 0 && len(streamErrors) > 0 {
		return nil, errors.Join(streamErrors...)
	}

	return entries, nil
}

func (c *Client) fetchLogStream(alloc *api.Allocation, allocID, task, stream string, fetchBytes int64) ([]store.LogEntry, error) {
	cancel := make(chan struct{})
	var once sync.Once
	closeCancel := func() { once.Do(func() { close(cancel) }) }
	defer closeCancel()

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	frames, errCh := c.client.AllocFS().Logs(alloc, false, task, stream, "end", fetchBytes, cancel, nil)

	var entries []store.LogEntry
	var lines frameLines
	firstFrame := true
	dropFirst := false
	emit := func(line, file string, offset int64) {
		if dropFirst {
			dropFirst = false
			return
		}
		entry := parseLogLine(line, alloc.JobID, allocID, task, stream)
		entry.LineRef = lineRef(file, offset)
		entries = append(entries, entry)
	}
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				lines.flush(emit)
				return entries, nil
			}
			if frame == nil {
				continue
			}
			if firstFrame && len(frame.Data) > 0 {
				firstFrame = false
				// A fetch that seeks into the middle of the file starts on
				// a presumed-partial line; drop it rather than storing a
				// fragment whose content shifts with the fetch window.
				dropFirst = frame.Offset > int64(len(frame.Data))
			}
			lines.add(frame, emit)
		case err, ok := <-errCh:
			if ok && err != nil && !isEOF(err) {
				return nil, fmt.Errorf("%s logs: %w", stream, err)
			}
			lines.flush(emit)
			return entries, nil
		case <-timer.C:
			closeCancel()
			return nil, fmt.Errorf("%s logs: timed out after 15 seconds", stream)
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

func lineRef(file string, offset int64) string {
	if file == "" {
		return ""
	}
	return file + "@" + strconv.FormatInt(offset, 10)
}

func (c *Client) StreamLogStreams(allocID, task string, streams []string, cancel <-chan struct{}) (<-chan store.LogEntry, <-chan error) {
	entryCh := make(chan store.LogEntry, 100)
	errBuffer := len(streams)
	if errBuffer < 1 {
		errBuffer = 1
	}
	errCh := make(chan error, errBuffer)

	go func() {
		defer close(entryCh)
		defer close(errCh)
		if len(streams) == 0 {
			errCh <- errors.New("at least one log stream is required")
			return
		}

		alloc, _, err := c.client.Allocations().Info(allocID, nil)
		if err != nil {
			errCh <- fmt.Errorf("get allocation %s: %w", allocID, err)
			return
		}

		nomadCancel := make(chan struct{})
		var once sync.Once
		closeCancel := func() { once.Do(func() { close(nomadCancel) }) }
		defer closeCancel()

		go func() {
			select {
			case <-cancel:
				closeCancel()
			case <-nomadCancel:
			}
		}()

		var wg sync.WaitGroup

		for _, stream := range streams {
			wg.Add(1)
			go func(stream string) {
				defer wg.Done()

				frames, streamErrCh := c.client.AllocFS().Logs(alloc, true, task, stream, "end", int64(0), nomadCancel, nil)

				var lines frameLines
				cancelled := false
				emit := func(line, file string, offset int64) {
					if cancelled {
						return
					}
					entry := parseLogLine(line, alloc.JobID, allocID, task, stream)
					entry.LineRef = lineRef(file, offset)
					select {
					case entryCh <- entry:
					case <-nomadCancel:
						cancelled = true
					}
				}

			streamLoop:
				for {
					select {
					case frame, ok := <-frames:
						if !ok {
							break streamLoop
						}
						if frame == nil {
							continue
						}
						lines.add(frame, emit)
						if cancelled {
							return
						}

					case err := <-streamErrCh:
						if err != nil && !isEOF(err) {
							select {
							case errCh <- err:
							default:
							}
						}
						return

					case <-nomadCancel:
						return
					}
				}

				lines.flush(emit)

				// The nomad api closes the frames channel on EOF/cancel
				// without ever sending on (or closing) its error channel,
				// and sends an error without closing frames otherwise. A
				// closed frames channel therefore means no error is coming;
				// blocking here would leak this goroutine forever.
				select {
				case err := <-streamErrCh:
					if err != nil && !isEOF(err) {
						select {
						case errCh <- err:
						default:
						}
					}
				default:
				}
			}(stream)
		}

		wg.Wait()
	}()

	return entryCh, errCh
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)
}

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

	entry.Timestamp = time.Now()
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
	return time.Now()
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

	if level == "" && !(msgOK && tsOK) {
		return time.Time{}, "", "", false
	}
	if !tsOK {
		ts = time.Now()
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
