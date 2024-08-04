NAME := ecsmec
SRCS := $(shell find . -type f -name '*.go' -not -name '*_test.go' -not -path './internal/testing/*')
MOCKS := internal/testing/capacitymock/mocks.go internal/testing/servicemock/mocks.go

all: bin/$(NAME)

bin/$(NAME): $(SRCS)
	go build -o bin/$(NAME)

.PHONY: clean
clean:
	rm -rf bin/$(NAME) $(MOCKS)

.PHONY: install
install:
	go install -ldflags "-s -w -X github.com/abicky/ecsmec/cmd.revision=$(shell git rev-parse --short HEAD)"

.PHONY: test
test: $(MOCKS)
	go test -v ./...

.PHONY: vet
vet: $(MOCKS)
	go vet ./...

$(MOCKS): $(SRCS)
	go generate ./...
# mockgen doesn't update timestamps if the generated code doesn't change
	touch $(MOCKS)
