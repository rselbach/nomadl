package nomad

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
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

	var buf bytes.Buffer
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return parseLogEntries(buf.String(), alloc.JobID, allocID, task, stream), nil
			}
			if frame != nil {
				buf.Write(frame.Data)
			}
		case err, ok := <-errCh:
			if ok && err != nil && !isEOF(err) {
				return nil, fmt.Errorf("%s logs: %w", stream, err)
			}
			return parseLogEntries(buf.String(), alloc.JobID, allocID, task, stream), nil
		case <-timer.C:
			closeCancel()
			return nil, fmt.Errorf("%s logs: timed out after 15 seconds", stream)
		}
	}
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

				var partial string
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
						partial += string(frame.Data)
						lines := strings.Split(partial, "\n")
						partial = lines[len(lines)-1]

						for _, line := range lines[:len(lines)-1] {
							if line == "" {
								continue
							}
							entry := parseLogLine(line, alloc.JobID, allocID, task, stream)
							select {
							case entryCh <- entry:
							case <-nomadCancel:
								return
							}
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

				if partial != "" {
					entry := parseLogLine(partial, alloc.JobID, allocID, task, stream)
					select {
					case entryCh <- entry:
					case <-nomadCancel:
					}
				}

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

func parseLogEntries(data, job, allocID, task, stream string) []store.LogEntry {
	lines := strings.Split(data, "\n")
	entries := make([]store.LogEntry, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		entries = append(entries, parseLogLine(line, job, allocID, task, stream))
	}

	return entries
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

	entry.Timestamp = time.Now()
	return entry
}

func extractTimestamp(j map[string]any) time.Time {
	for _, key := range []string{"timestamp", "time", "@timestamp", "ts", "datetime", "@time"} {
		if v, ok := j[key]; ok {
			if s, ok := v.(string); ok {
				for _, layout := range []string{
					time.RFC3339Nano,
					time.RFC3339,
					"2006-01-02T15:04:05.000Z",
					"2006-01-02T15:04:05Z",
					"2006-01-02 15:04:05",
					"2006-01-02 15:04:05.000",
				} {
					if t, err := time.Parse(layout, s); err == nil {
						return t
					}
				}
			}
		}
	}
	return time.Now()
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

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05.000000Z",
		time.UnixDate,
	}

	for _, layout := range layouts {
		if ts, err := time.Parse(layout, tsPart); err == nil {
			return ts, strings.ToUpper(levelStr), msg, true
		}
	}

	return time.Time{}, "", "", false
}

func isValidLevel(s string) bool {
	switch strings.ToUpper(s) {
	case "TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "ERR", "FATAL", "PANIC":
		return true
	default:
		return false
	}
}
