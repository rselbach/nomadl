build:
	go build ./...

test:
	go test -race ./...

# needs the nomad binary; runs a throwaway nomad agent -dev
e2e:
	NOMADL_E2E=1 go test -count=1 -run TestE2E -v ./internal/server/

lint:
	golangci-lint run ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: lint test vuln

run *args:
	go run . {{args}}
