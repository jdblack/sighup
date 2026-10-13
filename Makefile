BINARY        ?= sighup
GO            ?= go
CHART         ?= charts/sighup
# The image ships from ghcr (GitHub Container Registry).
IMAGE         ?= ghcr.io/jdblack/sighup
# The chart is pushed to its own OCI repo in Harbor, separate from the image tags.
HARBOR        ?= harbor.vn.linuxguru.net/library
HARBOR_CHARTS ?= $(HARBOR)/charts
# Pinned so lint/render do not inherit a default kubeVersion the chart's constraint rejects.
KUBE_VERSION  ?= 1.34.0
# The chart deliberately has no default targets -- a sighup must know nothing about any app -- so
# lint and render need a throwaway one to satisfy the guards.
CHART_TEST    ?= --set targets[0].repository=library/example \
                 --set targets[0].tag=edge \
                 --set targets[0].deployment=example

.PHONY: build tidy test fmt vet lint docker-build run-once clean \
        chart-lint chart-guard chart-template chart-package chart-push

build:        ## compile the binary
	$(GO) build -o $(BINARY) .

tidy:         ## sync go.mod and go.sum
	$(GO) mod tidy

test:         ## unit tests (no cluster required)
	$(GO) test ./...

fmt:          ## format
	$(GO) fmt ./...

vet:          ## vet
	$(GO) vet ./...

lint: fmt vet ## format + vet

docker-build: ## build the runtime image
	docker build -t $(IMAGE):dev .

run-once:     ## run `once` locally; export SIGHUP_REGISTRY and SIGHUP_TARGETS first
	$(GO) run . once

chart-lint:      ## lint the chart (strict)
	helm lint --strict $(CHART) --kube-version $(KUBE_VERSION) $(CHART_TEST)

chart-guard:     ## assert the chart refuses to render without targets
	@# `helm lint` only logs missing-required values as WARN and exits 0, so the guards need their
	@# own check: rendering with no targets MUST fail.
	@if helm template sighup $(CHART) --kube-version $(KUBE_VERSION) >/dev/null 2>&1; then \
		echo "FAIL: chart rendered with no targets -- a required guard is missing"; exit 1; \
	else \
		echo "OK: chart refuses to render without targets"; \
	fi

chart-template:  ## render the chart with a throwaway target
	helm template sighup $(CHART) --kube-version $(KUBE_VERSION) $(CHART_TEST)

chart-package:   ## package the chart -> sighup-<version>.tgz
	helm package $(CHART)

chart-push: chart-package ## package + push the chart to Harbor as OCI (needs: helm registry login)
	helm push sighup-*.tgz "oci://$(HARBOR_CHARTS)"
	@rm -f sighup-*.tgz

clean:
	rm -f $(BINARY)
