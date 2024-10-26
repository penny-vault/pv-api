build:
	go build

lint:
	go vet
	golangci-lint run

test:
	ginkgo run -race -cover ./...

clean:
	go clean
