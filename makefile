BINARY = rpi
VET_REPORT = vet.report
GOARCH = amd64

VERSION?=?
COMMIT=$(shell git rev-parse HEAD)
BRANCH=$(shell git rev-parse --abbrev-ref HEAD)

BUILD_BINARY=build/${BINARY}

# Setup the -ldflags option for go build here, interpolate the variable values
LDFLAGS = -ldflags "-X main.VERSION=${VERSION} -X main.COMMIT=${COMMIT} -X main.BRANCH=${BRANCH}"

# Build the project
all: clean vet arm darwin

arm:
	GOOS=linux GOARCH=arm GOARM=6 go build ${LDFLAGS} -o ${BUILD_BINARY}-arm .

darwin:
	GOOS=darwin GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BUILD_BINARY}-darwin-${GOARCH} .

vet:
	go vet ./... > ${VET_REPORT} 2>&1

fmt:
	go fmt $$(go list ./... | grep -v /vendor/)

clean:
	-rm -f ${VET_REPORT}
	-rm -f ${BUILD_BINARY}-*

.PHONY: arm darwin vet fmt clean
