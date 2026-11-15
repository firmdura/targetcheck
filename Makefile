# SPDX-License-Identifier: Apache-2.0
GO ?= go
VERSION ?= v1.0.0
DEV_IMAGE ?= targetcheck-dev:go1.27.4
TEST_CONTAINER_TARGETS ?= verify test-native test-arm demo dist
DEMO_CONTAINER_TARGETS ?= verify test-native test-arm demo

# Go's VCS stamping runs git, which refuses a checkout owned by another user,
# such as a bind mount in a root container ("detected dubious ownership").
# Binaries built by these targets are test helpers, or are stamped explicitly
# with -ldflags (demo, dist), so stamping is disabled for every target.
export GOFLAGS := -buildvcs=false $(GOFLAGS)

.PHONY: verify test-native test-arm prepare-demo demo dist test-container \
	check-environment check-format container-build verify-container demo-container

# Formatting, vet, race-enabled tests and the library and CLI build.
verify: check-environment check-format
	$(GO) vet ./...
	$(GO) test -race ./...
	$(GO) build ./...

check-environment:
	@test "$$(uname -s)" = Linux || { echo 'Use the pinned Linux container: make container-build and make prepare-demo (network), then make test-container (offline: verify, test-native, test-arm, demo and dist).' >&2; exit 1; }
	@test "$$($(GO) version | cut -d ' ' -f 3)" = go1.27.4 || { echo 'Go 1.27.4 is required.' >&2; exit 1; }

check-format:
	@files="$$(find cmd internal pkg examples testdata -type f -name '*.go')"; \
	bad="$$(gofmt -l $$files)" || exit $$?; \
	test -z "$$bad" || { echo "Run gofmt on: $$bad" >&2; exit 1; }

# No build context is transmitted; only this reviewed Dockerfile reaches Docker.
container-build:
	docker build --tag $(DEV_IMAGE) - < dev/Containerfile

# Real compiled helper processes and subprocess CLI acceptance, without network.
test-native: check-environment
	sh scripts/test-native.sh

# Actual cross-built ARM32 hard-float binaries under real qemu-arm through the
# CLI, without network. Missing QEMU, cross compiler or sysroot fails.
test-arm: check-environment
	GO=$(GO) sh scripts/test-arm.sh

# The only network step of the Wget example: fetch the pinned public source
# into the ignored cache and verify its SHA-256. Runs on Linux and macOS.
prepare-demo:
	sh scripts/prepare-demo.sh

# The mandatory real Wget native/ARM scenario matrix, offline after
# prepare-demo, run twice into fresh directories and compared.
demo: check-environment
	GO=$(GO) sh scripts/demo.sh

# Reproducible Linux amd64/arm64 release archives and SHA256SUMS in dist/,
# built only from committed content, then extracted, checked and smoke-tested.
dist: check-environment
	GO=$(GO) VERSION=$(VERSION) sh scripts/dist.sh

# Offline acceptance from a Linux or macOS Docker host: the committed snapshot,
# plus the verified Wget cache when demo runs, runs make $(TEST_CONTAINER_TARGETS)
# in the pinned image with --network=none. The log, demo evidence and dist/
# come back to .cache/test-container/, also when a target fails.
test-container:
	DEV_IMAGE=$(DEV_IMAGE) VERSION=$(VERSION) sh scripts/test-container.sh $(TEST_CONTAINER_TARGETS)

# Subsets of test-container. verify-container needs no Wget cache.
verify-container:
	DEV_IMAGE=$(DEV_IMAGE) VERSION=$(VERSION) sh scripts/test-container.sh verify test-native

demo-container:
	DEV_IMAGE=$(DEV_IMAGE) VERSION=$(VERSION) sh scripts/test-container.sh $(DEMO_CONTAINER_TARGETS)
