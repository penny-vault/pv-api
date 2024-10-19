build:
	go build

lint:
	go vet
	golangci-lint run

test:
	ginkgo run -race -cover --parallel=4 .../

clean:
	go clean
