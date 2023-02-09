GO                 ?= go
BINDIR := $(CURDIR)/bin
# version should match github.com/golang/mock version in go.mod
GOMOCK_VERSION := v1.6.0
GOMOCK_BINDIR  := .mockgen-bin
GOMOCK_BIN     := $(GOMOCK_BINDIR)/$(GOMOCK_VERSION)/mockgen
GOLANGCI_LINT_VERSION := v1.51.2
GOLANGCI_LINT_BINDIR  := .golangci-bin
GOLANGCI_LINT_BIN     := $(GOLANGCI_LINT_BINDIR)/$(GOLANGCI_LINT_VERSION)/golangci-lint

all: build

include versioning.mk

.PHONY: bin
bin:
	GOBIN=$(BINDIR) $(GO) install antrea.io/antrea-ui/...

.PHONY: test
test:
	$(GO) test -v ./...

# code linting
$(GOLANGCI_LINT_BIN):
	@echo "===> Installing Golangci-lint <==="
	@rm -rf $(GOLANGCI_LINT_BINDIR)/* # delete old versions
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOLANGCI_LINT_BINDIR)/$(GOLANGCI_LINT_VERSION) $(GOLANGCI_LINT_VERSION)

.PHONY: golangci
golangci: $(GOLANGCI_LINT_BIN)
	@echo "===> Running golangci <==="
	@GOOS=linux $(GOLANGCI_LINT_BIN) run -c .golangci.yml

.PHONY: golangci-fix
golangci-fix: $(GOLANGCI_LINT_BIN)
	@echo "===> Running golangci-fix <==="
	@GOOS=linux $(GOLANGCI_LINT_BIN) run -c .golangci.yml --fix

# mocks
$(GOMOCK_BIN):
	@echo "===> Installing mockgen <==="
	@rm -rf $(GOMOCK_BINDIR)/* # delete old versions
	GOBIN=$(CURDIR)/$(GOMOCK_BINDIR)/$(GOMOCK_VERSION) $(GO) install github.com/golang/mock/mockgen@$(GOMOCK_VERSION)

.PHONY: generate
generate: $(GOMOCK_BIN)
	PATH=$(CURDIR)/$(GOMOCK_BINDIR)/$(GOMOCK_VERSION):$$PATH $(GO) generate antrea.io/antrea-ui/...


.PHONY: clean
clean:
	rm -rf bin
	rm -rf $(GOLANGCI_LINT_BINDIR)
	rm -rf $(GOMOCK_BINDIR)

.PHONY: build-frontend
build-frontend:
	docker build -t antrea/antrea-ui-frontend:$(DOCKER_IMG_VERSION) -f build/Dockerfile.frontend .
	docker tag antrea/antrea-ui-frontend:$(DOCKER_IMG_VERSION) antrea/antrea-ui-frontend

.PHONY: build-backend
build-backend:
	docker build -t antrea/antrea-ui-backend:$(DOCKER_IMG_VERSION) -f build/Dockerfile.backend .
	docker tag antrea/antrea-ui-backend:$(DOCKER_IMG_VERSION) antrea/antrea-ui-backend

.PHONY: build
build: build-frontend build-backend
