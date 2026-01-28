RUNNER_BINARY="chaos-runner"
VERSION=1.0.0
BUILD=`date +%FT%T%z`
DIR=bin

PACKAGES=`go list ./... | grep -v /vendor/`
VETPACKAGES=`go list ./... | grep -v /vendor/ | grep -v /examples/`
GOFILES=`find . -name "*.go" -type f -not -path "./vendor/*"`

default: build-runner

build-runner:
	@mkdir -p ${DIR}
	@go build -ldflags "-X main.version=${VERSION}" -o ${DIR}/${RUNNER_BINARY} ./cmd/chaos-runner

build: build-runner

list:
	@echo ${PACKAGES}
	@echo ${VETPACKAGES}
	@echo ${GOFILES}

fmt:
	@gofmt -s -w ${GOFILES}

fmt-check:
	@diff=?(gofmt -s -d $(GOFILES)); \
	if [ -n "$$diff" ]; then \
		echo "Please run 'make fmt' and commit the result:"; \
		echo "$${diff}"; \
		exit 1; \
	fi;

test:
	@go test -cpu=1,2,4 -v -tags integration ./...

vet:
	@go vet $(VETPACKAGES)

clean:
	@rm -rf ${DIR}

.PHONY: default build-runner build fmt fmt-check test vet clean
