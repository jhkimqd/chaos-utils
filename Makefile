VERSION=1.0.0
BUILD=`date +%FT%T%z`
DIR=bin

PACKAGES=`go list ./... | grep -v /vendor/`
VETPACKAGES=`go list ./... | grep -v /vendor/ | grep -v /examples/`
GOFILES=`find . -name "*.go" -type f -not -path "./vendor/*"`

LDFLAGS=-ldflags "-X main.version=${VERSION}"
STATIC_FLAGS=CGO_ENABLED=0 GOOS=linux GOARCH=amd64
STATIC_LDFLAGS=-trimpath -ldflags="-s -w"

default: build-all

build-all: build-runner build-peer build-proxy

build-runner:
	@mkdir -p ${DIR}
	@go build ${LDFLAGS} -o ${DIR}/chaos-runner ./cmd/chaos-runner

build-peer:
	@mkdir -p ${DIR}
	@go build ${LDFLAGS} -o ${DIR}/chaos-peer ./cmd/chaos-peer

build-proxy:
	@mkdir -p ${DIR}
	@go build ${LDFLAGS} -o ${DIR}/corruption-proxy ./cmd/corruption-proxy

build-static:
	@mkdir -p ${DIR}
	@${STATIC_FLAGS} go build ${STATIC_LDFLAGS} -o ${DIR}/corruption-proxy ./cmd/corruption-proxy
	@${STATIC_FLAGS} go build ${STATIC_LDFLAGS} -o ${DIR}/chaos-peer ./cmd/chaos-peer

docker:
	docker build . --tag jhkimqd/chaos-utils:latest --file ./Dockerfile.chaos-utils

list:
	@echo ${PACKAGES}
	@echo ${VETPACKAGES}
	@echo ${GOFILES}

fmt:
	@gofmt -s -w ${GOFILES}

fmt-check:
	@diff=$$(gofmt -s -d $(GOFILES)); \
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

.PHONY: default build-all build-runner build-peer build-proxy build-static docker list fmt fmt-check test vet clean
