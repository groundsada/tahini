.PHONY: build run tidy docker-build helm-lint

BINARY := tahini
IMAGE  := tahini:latest

build:
	go build -o $(BINARY) ./cmd/tahini

run: build
	TAHINI_ADMIN_PASS=admin TAHINI_DATA_DIR=/tmp/tahini-dev ./$(BINARY)

tidy:
	go mod tidy

test:
	go test ./...

docker-build:
	docker build -t $(IMAGE) .

helm-lint:
	helm lint helm/tahini

helm-install:
	helm upgrade --install tahini helm/tahini \
	  --namespace tahini --create-namespace \
	  --set auth.adminPass=changeme
