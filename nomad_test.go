package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListServices(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.Equal("/v1/services", request.URL.Path)
		r.Equal("dev", request.URL.Query().Get("namespace"))
		r.Equal("global", request.URL.Query().Get("region"))
		r.Equal("secret", request.Header.Get("X-Nomad-Token"))

		_, err := w.Write([]byte(`{
			"web": [
				{"Tags": ["http", "blue"], "Provider": "nomad"},
				{"Tags": ["http"], "Provider": "nomad"}
			],
			"api": [{"Tags": ["grpc"], "Provider": "consul"}]
		}`))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{
		addr:      server.URL,
		token:     "secret",
		namespace: "dev",
		region:    "global",
	})
	r.NoError(err)

	services, err := client.ListServices(t.Context())
	r.NoError(err)
	r.Equal([]serviceSummary{
		{Name: "api", Tags: []string{"grpc"}, Provider: "consul", Source: "service", Instances: 1},
		{Name: "web", Tags: []string{"blue", "http"}, Provider: "nomad", Source: "service", Instances: 2},
	}, services)
}

func TestListServicesHandlesArrayResponse(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.Equal("/v1/services", request.URL.Path)

		_, err := w.Write([]byte(`[
			{"Name": "web", "Tags": ["http", "blue", "http"], "Provider": "nomad"},
			{"Name": "api", "Tags": ["grpc"], "Provider": "consul"}
		]`))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	services, err := client.ListServices(t.Context())
	r.NoError(err)
	r.Equal([]serviceSummary{
		{Name: "api", Tags: []string{"grpc"}, Provider: "consul", Source: "service", Instances: 1},
		{Name: "web", Tags: []string{"blue", "http"}, Provider: "nomad", Source: "service", Instances: 1},
	}, services)
}

func TestListServicesFallsBackToJobs(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/services":
			_, err := w.Write([]byte(`[]`))
			r.NoError(err)
		case "/v1/jobs":
			_, err := w.Write([]byte(`[
				{"ID": "web", "Name": "web", "Type": "service", "Status": "running", "JobSummary": {"Summary": {"web": {"Running": 2}}}},
				{"ID": "seed", "Name": "seed", "Type": "batch", "Status": "running", "JobSummary": {"Summary": {"seed": {"Running": 0}}}},
				{"ID": "old", "Name": "old", "Type": "service", "Status": "dead", "JobSummary": {"Summary": {"old": {"Running": 1}}}}
			]`))
			r.NoError(err)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	services, err := client.ListServices(t.Context())
	r.NoError(err)
	r.Equal([]serviceSummary{
		{Name: "web", Provider: "nomad", Source: "job", Type: "service", Status: "running", Instances: 2},
	}, services)
}

func TestJobInstances(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.Equal("/v1/job/web/allocations", request.URL.Path)
		_, err := w.Write([]byte(`[
			{"ID": "alloc-a", "JobID": "web", "TaskGroup": "api", "DesiredStatus": "run", "ClientStatus": "running", "TaskStates": {"api": {"State": "running"}, "init": {"State": "dead"}}},
			{"ID": "alloc-b", "JobID": "web", "TaskGroup": "worker", "DesiredStatus": "run", "ClientStatus": "pending", "TaskStates": {"worker": {"State": "pending"}}},
			{"ID": "alloc-c", "JobID": "web", "TaskGroup": "worker", "DesiredStatus": "stop", "ClientStatus": "running", "TaskStates": {"worker": {"State": "running"}}}
		]`))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	instances, err := client.JobInstances(t.Context(), "web")
	r.NoError(err)
	r.Equal([]serviceInstance{
		{AllocID: "alloc-a", JobID: "web", Task: "api", Group: "api", Status: "running"},
	}, instances)
}

func TestServiceInstancesDeduplicatesAllocTaskPairs(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.Equal("/v1/service/web", request.URL.Path)
		_, err := w.Write([]byte(`[
			{"AllocID": "alloc-b", "JobID": "job", "TaskName": "app", "Group": "grp", "Address": "10.0.0.2", "Port": 8080, "Status": "passing"},
			{"AllocID": "alloc-a", "JobID": "job", "TaskName": "app", "Group": "grp", "Address": "10.0.0.1", "Port": 8080, "Status": "passing"},
			{"AllocID": "alloc-a", "JobID": "job", "TaskName": "app", "Group": "grp", "Address": "10.0.0.1", "Port": 8081, "Status": "passing"},
			{"AllocID": "", "JobID": "job", "TaskName": "app"}
		]`))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	instances, err := client.ServiceInstances(t.Context(), "web")
	r.NoError(err)
	r.Equal([]serviceInstance{
		{AllocID: "alloc-a", JobID: "job", Task: "app", Group: "grp", Address: "10.0.0.1", Port: 8080, Status: "passing"},
		{AllocID: "alloc-b", JobID: "job", Task: "app", Group: "grp", Address: "10.0.0.2", Port: 8080, Status: "passing"},
	}, instances)
}

func TestServiceInstancesResolvesGroupServiceTasks(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/service/web":
			_, err := w.Write([]byte(`[
				{"AllocID": "alloc-a", "JobID": "job", "TaskName": "", "Group": "grp", "Address": "10.0.0.1", "Port": 8080, "Status": "passing"}
			]`))
			r.NoError(err)
		case "/v1/allocation/alloc-a":
			_, err := w.Write([]byte(`{
				"TaskStates": {
					"api": {},
					"worker": {}
				}
			}`))
			r.NoError(err)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	instances, err := client.ServiceInstances(t.Context(), "web")
	r.NoError(err)
	r.Equal([]serviceInstance{
		{AllocID: "alloc-a", JobID: "job", Task: "api", Group: "grp", Address: "10.0.0.1", Port: 8080, Status: "passing"},
		{AllocID: "alloc-a", JobID: "job", Task: "worker", Group: "grp", Address: "10.0.0.1", Port: 8080, Status: "passing"},
	}, instances)
}

func TestAllocationTasks(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.Equal("/v1/allocation/alloc-1", request.URL.Path)
		_, err := w.Write([]byte(`{"TaskStates": {"worker": {}, "api": {}}}`))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	tasks, err := client.AllocationTasks(t.Context(), "alloc-1")
	r.NoError(err)
	r.Equal([]string{"api", "worker"}, tasks)
}

func TestResponseErrorIncludesBody(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte("permission denied by Señor Chang"))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	_, err = client.ListServices(t.Context())
	r.Error(err)
	r.True(strings.Contains(err.Error(), "403 Forbidden"))
	r.True(strings.Contains(err.Error(), "permission denied by Señor Chang"))
}

func TestStreamLogsUsesNomadLogEndpoint(t *testing.T) {
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.Equal("/v1/client/fs/logs/alloc-1", request.URL.Path)
		r.Equal("app", request.URL.Query().Get("task"))
		r.Equal("stdout", request.URL.Query().Get("type"))
		r.Equal("end", request.URL.Query().Get("origin"))
		r.Equal("123", request.URL.Query().Get("offset"))
		r.Equal("true", request.URL.Query().Get("plain"))
		r.Equal("true", request.URL.Query().Get("follow"))

		_, err := w.Write([]byte("one\ntwo\n"))
		r.NoError(err)
	}))
	defer server.Close()

	client, err := newNomadClient(nomadConfig{addr: server.URL})
	r.NoError(err)

	lines := make(chan logLine, 2)
	source := logSource{Service: "web", AllocID: "alloc-1", Task: "app", Stream: "stdout"}
	err = client.StreamLogs(t.Context(), source, 123, lines)
	r.NoError(err)

	r.Equal(logLine{Source: source, Text: "one"}, <-lines)
	r.Equal(logLine{Source: source, Text: "two"}, <-lines)
}

func TestTailOffset(t *testing.T) {
	r := require.New(t)

	tests := map[string]struct {
		tailBytes int64
		want      string
	}{
		"future only": {
			tailBytes: 0,
			want:      "0",
		},
		"recent bytes": {
			tailBytes: 4096,
			want:      "4096",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r.Equal(tc.want, tailOffset(tc.tailBytes))
		})
	}
}
