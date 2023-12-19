# Makefile

.DEFAULT_GOAL := build

ARCH ?= amd64

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -o traffic-checker main.go

run:
	go run main.go

docker-build:
	docker build -t traffic-checker:v1 --platform=linux/$(ARCH) .

docker-run:
	docker run -p 8080:8080 --platform=linux/$(ARCH) traffic-checker:v1

clean:
	rm -f traffic-checker

.PHONY: build run docker-build docker-run clean