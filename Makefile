GO                 ?= go
# read Go version from go,mod file
GO_VERSION         := $(shell grep '^go.*$$' go.mod | awk '{print $$2}')
LDFLAGS            :=
GOFLAGS            :=
BINDIR := $(CURDIR)/bin
# version should match github.com/golang/mock version in go.mod
GOMOCK_VERSION := $(shell grep '^\s*github.com\/golang\/mock\sv\S*$$' go.mod | awk '{print $$2}')
GOMOCK_BINDIR  := .mockgen-bin
GOMOCK_BIN     := $(GOMOCK_BINDIR)/$(GOMOCK_VERSION)/mockgen
GOLANGCI_LINT_VERSION := v1.51.2
GOLANGCI_LINT_BINDIR  := .golangci-bin
GOLANGCI_LINT_BIN     := $(GOLANGCI_LINT_BINDIR)/$(GOLANGCI_LINT_VERSION)/golangci-lint

all: build

include versioning.mk
VERSION_LDFLAGS = -X antrea.io/antrea-ui/pkg/version.Version=$(VERSION)
VERSION_LDFLAGS += -X antrea.io/antrea-ui/pkg/version.GitSHA=$(GIT_SHA)
VERSION_LDFLAGS += -X antrea.io/antrea-ui/pkg/version.GitTreeState=$(GIT_TREE_STATE)
VERSION_LDFLAGS += -X antrea.io/antrea-ui/pkg/version.ReleaseStatus=$(RELEASE_STATUS)
LDFLAGS += $(VERSION_LDFLAGS)

.PHONY: bin
bin:
	GOBIN=$(BINDIR) $(GO) install $(GOFLAGS) -ldflags '-s -w $(LDFLAGS)' antrea.io/antrea-ui/...

.PHONY: test
test:
	$(GO) test -race -v ./...

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
	PATH=$(CURDIR)/$(GOMOCK_BINDIR)/$(GOMOCK_VERSION):$$PATH MOCKGEN_COPYRIGHT_FILE=$(CURDIR)/hack/boilerplate/license_header.raw.txt $(GO) generate antrea.io/antrea-ui/...


.PHONY: clean
clean:
	rm -rf bin
	rm -rf $(GOLANGCI_LINT_BINDIR)
	rm -rf $(GOMOCK_BINDIR)

.PHONY: build-frontend
build-frontend:
	docker build --pull -t antrea/antrea-ui-frontend:$(DOCKER_IMG_VERSION) -f build/frontend.dockerfile --build-arg GO_VERSION=$(GO_VERSION) .
	docker tag antrea/antrea-ui-frontend:$(DOCKER_IMG_VERSION) antrea/antrea-ui-frontend

.PHONY: build-backend
build-backend:
	docker build --pull -t antrea/antrea-ui-backend:$(DOCKER_IMG_VERSION) -f build/backend.dockerfile --build-arg GO_VERSION=$(GO_VERSION) .
	docker tag antrea/antrea-ui-backend:$(DOCKER_IMG_VERSION) antrea/antrea-ui-backend

.PHONY: build
build: build-frontend build-backend

.PHONY: check-copyright
check-copyright:
	@GO=$(GO) $(CURDIR)/hack/add-license.sh

.PHONY: add-copyright
add-copyright:
	@GO=$(GO) $(CURDIR)/hack/add-license.sh --add
