build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: lint test vuln

run *args:
	go run . {{args}}
