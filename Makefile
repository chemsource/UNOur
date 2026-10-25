.PHONY: run build test race vet check deploy-test bench fuzz

run:
	go run -buildvcs=false ./cmd/server

build:
	go build -buildvcs=false -trimpath -o bin/uno-server ./cmd/server
	go build -buildvcs=false -trimpath -o bin/unour-admin-config ./cmd/admin-config

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

check: test vet race deploy-test

deploy-test:
	python3 -B -m unittest discover -s deploy/tests -v

bench:
	go test ./game -run '^$$' -bench . -benchmem

fuzz:
	go test ./game -run '^$$' -fuzz FuzzCommandsPreserveCards -fuzztime 10s -parallel 2
