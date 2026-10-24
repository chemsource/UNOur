.PHONY: run build test race vet check bench fuzz

run:
	go run -buildvcs=false ./cmd/server

build:
	go build -buildvcs=false -trimpath -o bin/uno-server ./cmd/server

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

check: test vet race

bench:
	go test ./game -run '^$$' -bench . -benchmem

fuzz:
	go test ./game -run '^$$' -fuzz FuzzCommandsPreserveCards -fuzztime 10s -parallel 2
