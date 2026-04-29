package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

type nomadConfig struct {
	addr       string
	token      string
	namespace  string
	region     string
	httpClient *http.Client
}

type nomadClient struct {
	baseURL    *url.URL
	token      string
	namespace  string
	region     string
	httpClient *http.Client
}

type serviceSummary struct {
	Name      string
	Tags      []string
	Provider  string
	Source    string
	Type      string
	Status    string
	Instances int
}

type serviceInstance struct {
	AllocID string
	JobID   string
	Task    string
	Group   string
	Address string
	Port    int
	Status  string
}

type logSource struct {
	Service string
	AllocID string
	JobID   string
	Task    string
	Stream  string
}

type logLine struct {
	Source logSource
	Text   string
}

func newNomadClient(config nomadConfig) (*nomadClient, error) {
	addr := strings.TrimSpace(config.addr)
	if addr == "" {
		return nil, fmt.Errorf("nomad address is required")
	}

	baseURL, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("parse Nomad address: %w", err)
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("nomad address must include scheme and host, got %q", addr)
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")

	httpClient := config.httpClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	return &nomadClient{
		baseURL:    baseURL,
		token:      config.token,
		namespace:  config.namespace,
		region:     config.region,
		httpClient: httpClient,
	}, nil
}

func (client *nomadClient) ListServices(ctx context.Context) ([]serviceSummary, error) {
	requestURL := client.endpoint("/v1/services", nil)
	request, err := client.newRequest(ctx, http.MethodGet, requestURL)
	if err != nil {
		return nil, err
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list Nomad services: %w", err)
	}
	defer closeBody(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, client.responseError(response, "list Nomad services")
	}

	services, err := decodeServices(response.Body)
	if err != nil {
		return nil, fmt.Errorf("decode Nomad services: %w", err)
	}
	if len(services) == 0 {
		return client.ListJobs(ctx)
	}
	return services, nil
}

func (client *nomadClient) ListJobs(ctx context.Context) ([]serviceSummary, error) {
	requestURL := client.endpoint("/v1/jobs", nil)
	request, err := client.newRequest(ctx, http.MethodGet, requestURL)
	if err != nil {
		return nil, err
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list Nomad jobs: %w", err)
	}
	defer closeBody(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, client.responseError(response, "list Nomad jobs")
	}

	var payload []struct {
		ID         string `json:"ID"`
		Name       string `json:"Name"`
		Type       string `json:"Type"`
		Status     string `json:"Status"`
		JobSummary struct {
			Summary map[string]struct {
				Running int `json:"Running"`
			} `json:"Summary"`
		} `json:"JobSummary"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Nomad jobs: %w", err)
	}

	services := make([]serviceSummary, 0, len(payload))
	for _, job := range payload {
		if job.ID == "" || job.Status == "dead" {
			continue
		}

		running := 0
		for _, group := range job.JobSummary.Summary {
			running += group.Running
		}
		if running == 0 {
			continue
		}

		name := job.Name
		if name == "" {
			name = job.ID
		}

		services = append(services, serviceSummary{
			Name:      name,
			Provider:  "nomad",
			Source:    "job",
			Type:      job.Type,
			Status:    job.Status,
			Instances: running,
		})
	}
	sortServices(services)
	return services, nil
}

func decodeServices(reader io.Reader) ([]serviceSummary, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(reader).Decode(&raw); err != nil {
		return nil, err
	}

	var byName map[string][]struct {
		Tags     []string `json:"Tags"`
		Provider string   `json:"Provider"`
	}
	if err := json.Unmarshal(raw, &byName); err == nil && byName != nil {
		return servicesFromMap(byName), nil
	}

	var summaries []struct {
		Name      string   `json:"Name"`
		Tags      []string `json:"Tags"`
		Provider  string   `json:"Provider"`
		Instances int      `json:"Instances"`
	}
	if err := json.Unmarshal(raw, &summaries); err != nil {
		return nil, err
	}

	services := make([]serviceSummary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Name == "" {
			continue
		}

		instances := summary.Instances
		if instances == 0 {
			instances = 1
		}

		services = append(services, serviceSummary{
			Name:      summary.Name,
			Tags:      sortedStrings(summary.Tags),
			Provider:  summary.Provider,
			Source:    "service",
			Instances: instances,
		})
	}
	sortServices(services)
	return services, nil
}

func servicesFromMap(payload map[string][]struct {
	Tags     []string `json:"Tags"`
	Provider string   `json:"Provider"`
}) []serviceSummary {
	services := make([]serviceSummary, 0, len(payload))
	for name, registrations := range payload {
		if name == "" {
			continue
		}

		tags := make(map[string]struct{})
		provider := ""
		for _, registration := range registrations {
			if provider == "" {
				provider = registration.Provider
			}
			for _, tag := range registration.Tags {
				tags[tag] = struct{}{}
			}
		}

		services = append(services, serviceSummary{
			Name:      name,
			Tags:      sortedKeys(tags),
			Provider:  provider,
			Source:    "service",
			Instances: len(registrations),
		})
	}

	sortServices(services)
	return services
}

func sortServices(services []serviceSummary) {
	sort.Slice(services, func(i int, j int) bool {
		return services[i].Name < services[j].Name
	})
}

func (client *nomadClient) TargetInstances(ctx context.Context, target serviceSummary) ([]serviceInstance, error) {
	if target.Source == "job" {
		return client.JobInstances(ctx, target.Name)
	}
	return client.ServiceInstances(ctx, target.Name)
}

func (client *nomadClient) JobInstances(ctx context.Context, jobID string) ([]serviceInstance, error) {
	requestURL := client.endpoint("/v1/job/"+url.PathEscape(jobID)+"/allocations", nil)
	request, err := client.newRequest(ctx, http.MethodGet, requestURL)
	if err != nil {
		return nil, err
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get Nomad job %q allocations: %w", jobID, err)
	}
	defer closeBody(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, client.responseError(response, "get Nomad job %q allocations", jobID)
	}

	var payload []struct {
		ID            string `json:"ID"`
		JobID         string `json:"JobID"`
		TaskGroup     string `json:"TaskGroup"`
		DesiredStatus string `json:"DesiredStatus"`
		ClientStatus  string `json:"ClientStatus"`
		TaskStates    map[string]struct {
			State string `json:"State"`
		} `json:"TaskStates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Nomad job %q allocations: %w", jobID, err)
	}

	instances := make([]serviceInstance, 0, len(payload))
	seen := make(map[string]struct{})
	for _, allocation := range payload {
		if allocation.ID == "" || allocation.DesiredStatus != "run" || allocation.ClientStatus != "running" {
			continue
		}

		for _, task := range runningTasks(allocation.TaskStates) {
			key := allocation.ID + ":" + task
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			instances = append(instances, serviceInstance{
				AllocID: allocation.ID,
				JobID:   allocation.JobID,
				Task:    task,
				Group:   allocation.TaskGroup,
				Status:  allocation.ClientStatus,
			})
		}
	}
	sort.Slice(instances, func(i int, j int) bool {
		left := instances[i].JobID + instances[i].AllocID + instances[i].Task
		right := instances[j].JobID + instances[j].AllocID + instances[j].Task
		return left < right
	})
	return instances, nil
}

func runningTasks(taskStates map[string]struct {
	State string `json:"State"`
}) []string {
	tasks := make([]string, 0, len(taskStates))
	for task, state := range taskStates {
		if task != "" && state.State == "running" {
			tasks = append(tasks, task)
		}
	}
	sort.Strings(tasks)
	return tasks
}

func (client *nomadClient) ServiceInstances(ctx context.Context, service string) ([]serviceInstance, error) {
	requestURL := client.endpoint("/v1/service/"+url.PathEscape(service), nil)
	request, err := client.newRequest(ctx, http.MethodGet, requestURL)
	if err != nil {
		return nil, err
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get Nomad service %q: %w", service, err)
	}
	defer closeBody(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, client.responseError(response, "get Nomad service %q", service)
	}

	var payload []struct {
		AllocID string `json:"AllocID"`
		JobID   string `json:"JobID"`
		Task    string `json:"TaskName"`
		Group   string `json:"Group"`
		Address string `json:"Address"`
		Port    int    `json:"Port"`
		Status  string `json:"Status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Nomad service %q: %w", service, err)
	}

	instances := make([]serviceInstance, 0, len(payload))
	seen := make(map[string]struct{})
	allocationTasks := make(map[string][]string)
	for _, registration := range payload {
		if registration.AllocID == "" {
			continue
		}

		tasks := []string{registration.Task}
		if registration.Task == "" {
			var ok bool
			tasks, ok = allocationTasks[registration.AllocID]
			if !ok {
				tasks, err = client.AllocationTasks(ctx, registration.AllocID)
				if err != nil {
					return nil, fmt.Errorf("resolve tasks for allocation %s: %w", registration.AllocID, err)
				}
				allocationTasks[registration.AllocID] = tasks
			}
		}

		for _, task := range tasks {
			if task == "" {
				continue
			}

			key := registration.AllocID + ":" + task
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			instances = append(instances, serviceInstance{
				AllocID: registration.AllocID,
				JobID:   registration.JobID,
				Task:    task,
				Group:   registration.Group,
				Address: registration.Address,
				Port:    registration.Port,
				Status:  registration.Status,
			})
		}
	}

	sort.Slice(instances, func(i int, j int) bool {
		left := instances[i].JobID + instances[i].AllocID + instances[i].Task
		right := instances[j].JobID + instances[j].AllocID + instances[j].Task
		return left < right
	})

	return instances, nil
}

func (client *nomadClient) AllocationTasks(ctx context.Context, allocID string) ([]string, error) {
	requestURL := client.endpoint("/v1/allocation/"+url.PathEscape(allocID), nil)
	request, err := client.newRequest(ctx, http.MethodGet, requestURL)
	if err != nil {
		return nil, err
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get Nomad allocation %q: %w", allocID, err)
	}
	defer closeBody(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, client.responseError(response, "get Nomad allocation %q", allocID)
	}

	var payload struct {
		TaskStates map[string]json.RawMessage `json:"TaskStates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Nomad allocation %q: %w", allocID, err)
	}

	tasks := make([]string, 0, len(payload.TaskStates))
	for task := range payload.TaskStates {
		if task != "" {
			tasks = append(tasks, task)
		}
	}
	sort.Strings(tasks)
	return tasks, nil
}

func (client *nomadClient) StreamLogs(ctx context.Context, source logSource, tailBytes int64, lines chan<- logLine) error {
	query := url.Values{
		"task":   []string{source.Task},
		"type":   []string{source.Stream},
		"origin": []string{"end"},
		"offset": []string{tailOffset(tailBytes)},
		"plain":  []string{"true"},
		"follow": []string{"true"},
	}

	requestURL := client.endpoint("/v1/client/fs/logs/"+url.PathEscape(source.AllocID), query)
	request, err := client.newRequest(ctx, http.MethodGet, requestURL)
	if err != nil {
		return err
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("stream %s logs for %s/%s: %w", source.Stream, source.AllocID, source.Task, err)
	}
	defer closeBody(response.Body)

	if response.StatusCode != http.StatusOK {
		return client.responseError(response, "stream %s logs for %s/%s", source.Stream, source.AllocID, source.Task)
	}

	scanner := bufio.NewScanner(response.Body)
	buffer := make([]byte, 0, 1024*1024)
	scanner.Buffer(buffer, 8*1024*1024)
	for scanner.Scan() {
		select {
		case lines <- logLine{Source: source, Text: scanner.Text()}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s logs for %s/%s: %w", source.Stream, source.AllocID, source.Task, err)
	}

	return nil
}

func tailOffset(tailBytes int64) string {
	if tailBytes <= 0 {
		return "0"
	}
	return fmt.Sprintf("%d", tailBytes)
}

func (client *nomadClient) endpoint(path string, query url.Values) string {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(client.baseURL.Path, "/") + path
	if query == nil {
		query = url.Values{}
	}
	if client.namespace != "" {
		query.Set("namespace", client.namespace)
	}
	if client.region != "" {
		query.Set("region", client.region)
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func (client *nomadClient) newRequest(ctx context.Context, method string, requestURL string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create Nomad request: %w", err)
	}
	if client.token != "" {
		request.Header.Set("X-Nomad-Token", client.token)
	}
	return request, nil
}

func (client *nomadClient) responseError(response *http.Response, format string, args ...any) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024))
	if err != nil {
		return fmt.Errorf(format+": HTTP %s; failed to read response body: %w", append(args, response.Status, err)...)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf(format+": HTTP %s: %s", append(args, response.Status, message)...)
}

func closeBody(body io.Closer) {
	if err := body.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "nomad-logs: close response body: %v\n", err)
	}
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	return sortedKeys(seen)
}
