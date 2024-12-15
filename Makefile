EXECUTABLE_NAME = pvapi
GO_MODULE = github.com/penny-vault/pv-api

GIT_VERSION:=$$(git describe --always | awk '{n=split($$0, a, "-"); if (n=="3") { split(a[1], b, "."); print b[1] "." b[2]+1 "." b[3] "-pre+" a[2] "-" a[3] } else { print a[1] }}')
COMMIT_HASH:=$$(git rev-parse --short HEAD)
BUILD_DATE:=$$(date -Iseconds)

build:
	go build -o ${EXECUTABLE_NAME} -ldflags "-X $(GO_MODULE)/pkginfo.Version=$(GIT_VERSION) -X $(GO_MODULE)/pkginfo.BuildDate=$(BUILD_DATE) -X $(GO_MODULE)/pkginfo.CommitHash=$(COMMIT_HASH) -X $(GO_MODULE)/pkginfo.ProgramName=$(EXECUTABLE_NAME)"

lint:
	go vet
	golangci-lint run

test:
	ginkgo run -race -cover ./...

clean:
	go clean
