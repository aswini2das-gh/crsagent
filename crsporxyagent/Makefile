# CRSProxyAgent Makefile
# Standard kubebuilder-style targets for building, testing, and deploying

IMG ?= crsproxyagent:dev
CONTROLLER_GEN ?= controller-gen
CLUSTER_ID ?= dev-cluster-01
NAMESPACE ?= crs-system
# Default to Never: docker-build loads the image straight into the local
# Docker Desktop daemon, so Kubernetes should use that local image rather
# than trying to pull it from a registry. Override for a real registry, e.g.:
#   make helm-install IMG=ghcr.io/you/crsproxyagent:1.0.0 PULL_POLICY=IfNotPresent
PULL_POLICY ?= Never
IMG_REPO := $(word 1,$(subst :, ,$(IMG)))
IMG_TAG := $(word 2,$(subst :, ,$(IMG)))

.PHONY: all build test test-race vet lint generate manifests fmt \
	docker-build docker-push helm-deps helm-install helm-uninstall

all: build

## Build the operator binary
build:
	go build -o bin/crsproxyagent cmd/main.go

## Run unit tests with coverage
test:
	go test ./... -coverprofile cover.out

## Run tests with race detector
test-race:
	go test -race ./... -count=1

## Run go vet
vet:
	go vet ./...

## Run golangci-lint (if available)
lint:
	golangci-lint run

## Generate code (deepcopy, etc.)
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

## Generate CRD manifests and RBAC
manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=crsproxyagent-role webhook paths="./..." output:crd:artifacts:config=deploy/helm/crsproxyagent/crds

## Run go fmt
fmt:
	go fmt ./...

## Build Docker image
docker-build:
	docker build -t $(IMG) .

## Push Docker image
docker-push:
	docker push $(IMG)

## Fetch chart dependencies (Kyverno sub-chart) - fully self-contained,
## adds/updates the Kyverno Helm repo itself so no manual steps are needed.
## The leading "-" tells make to ignore a non-zero exit (e.g. "repo already
## exists") instead of relying on Unix-only "|| true" / "/dev/null", which
## don't exist under Windows' cmd.exe (make's default shell on Windows).
helm-deps:
	-helm repo add kyverno https://kyverno.github.io/kyverno/
	helm repo update
	helm dependency update deploy/helm/crsproxyagent/

## One-shot install via Helm: fetches dependencies (Kyverno) and deploys
## node discovery + Kyverno + telemetry together as a single release, using
## the locally built image (see IMG / PULL_POLICY above).
## Override CLUSTER_ID on the command line if needed, e.g.:
##   make helm-install CLUSTER_ID=my-seller-cluster
helm-install: helm-deps
	helm upgrade --install crsproxyagent deploy/helm/crsproxyagent/ --namespace $(NAMESPACE) --create-namespace --set clusterID=$(CLUSTER_ID) --set operator.image.repository=$(IMG_REPO) --set operator.image.tag=$(IMG_TAG) --set operator.image.pullPolicy=$(PULL_POLICY) --wait --timeout 600s

## Uninstall via Helm
helm-uninstall:
	helm uninstall crsproxyagent --namespace $(NAMESPACE)
