build:
	go build ./...

test:
	go test ./...

run *args:
	go run . {{args}}
