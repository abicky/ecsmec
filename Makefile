NAME := ecsmec
SRCS := $(shell find . -type f -name '*.go' -not -name '*_test.go')

all: bin/$(NAME)

bin/$(NAME): $(SRCS)
	go build -o bin/$(NAME)

.PHONY: clean
clean:
	rm -rf bin/$(NAME)

.PHONY: install
install:
	go install -ldflags "-s -w -X github.com/abicky/ecsmec/cmd.revision=$(shell git rev-parse --short HEAD)"

.PHONY: test
test:
	go test -v ./...
