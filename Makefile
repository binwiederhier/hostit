VERSION ?= 0.1.0
GO      ?= go
NPM     ?= npm

.PHONY: help build web web-deps web-build release release-snapshot check test e2e vet fmt fmt-check clean deps install install-deb purge-package

help:
	@echo "Build:"
	@echo "  make web                - Build the React web app into control/site (needed before build)"
	@echo "  make build              - Build a local dev binary to dist/hostit"
	@echo
	@echo "Test/check:"
	@echo "  make check              - Run tests, formatting checks and vetting"
	@echo "  make test               - Run tests"
	@echo "  make e2e                - Run end-to-end tests against a running server (RUN=... to select)"
	@echo "  make e2e-smoke          - Fast e2e smoke subset (~4 min)"
	@echo "  make vet                - Run 'go vet'"
	@echo "  make fmt                - Run 'gofmt -s -w'"
	@echo "  make fmt-check          - Run 'gofmt', but don't change anything"
	@echo "  make clean              - Remove dist/ folder"
	@echo
	@echo "Releasing (requires goreleaser and a git repository):"
	@echo "  make release            - Create a release"
	@echo "  make release-snapshot   - Create a test release"
	@echo
	@echo "Install locally (requires sudo):"
	@echo "  make install            - Copy dev binary to /usr/bin/hostit"
	@echo "  make install-deb        - Install the .deb from dist/ (amd64)"

build:
	$(GO) build -o dist/hostit .

web: web-deps web-build

web-deps:
	cd web && $(NPM) install

# The built app is embedded via control/site (see control/web.go). Everything
# there is generated and gitignored; .gitkeep is the one tracked file, so it is
# put back after the wipe -- deleting it would leave the tree dirty, which is
# exactly what this arrangement exists to avoid.
web-build:
	cd web && $(NPM) run build
	rm -rf control/site
	mkdir -p control/site
	cp -r web/build/. control/site/
	touch control/site/.gitkeep

release: clean deps web check changelog-check
	goreleaser release --clean

# A release is not complete until CHANGELOG.md describes it, so the check is a
# prerequisite of `release` rather than a habit. Snapshots skip it: they are
# untagged builds for stage, not releases.
changelog-check:
	scripts/changelog-check.sh

release-snapshot: clean deps web check
	goreleaser release --snapshot --clean

check: test fmt-check vet

test:
	$(GO) test ./...

# End-to-end tests against a RUNNING server; they create and delete e2e-* apps,
# so point them at a test instance. RUN= selects tests (go test -run syntax):
#   HOSTIT_HOST=https://hostit.apps.example.com HOSTIT_TOKEN=... make e2e
#   HOSTIT_HOST=... HOSTIT_TOKEN=... make e2e RUN='TestFork|TestChurn'
RUN ?= .
# E2E_PARALLEL caps how many e2e tests run concurrently: most tests are
# self-contained (own app, unique name) and marked t.Parallel(), but each app
# create is heavy on the server (btrfs, podman), so this is a server-load
# knob, not a client one. Global-state tests (disk default, assistant
# settings) stay serial and complete before the parallel batch starts.
E2E_PARALLEL ?= 4

e2e:
	@test -n "$(HOSTIT_HOST)" || { echo "set HOSTIT_HOST and HOSTIT_TOKEN"; exit 1; }
	$(GO) test -tags e2e -count 1 -timeout 30m -parallel $(E2E_PARALLEL) -run '$(RUN)' -v ./e2e/

# A fast smoke subset (~4 min): one full create-deploy-serve journey, the token
# scoping boundary, and the preview contract. For quick confidence between full runs.
e2e-smoke:
	@test -n "$(HOSTIT_HOST)" || { echo "set HOSTIT_HOST and HOSTIT_TOKEN"; exit 1; }
	$(GO) test -tags e2e -count 1 -timeout 10m -run 'TestAgentCanBuildAnAppFromNothing|TestAppTokenCannotLeaveItsApp|TestAppPreviewModeContract' -v ./e2e/

vet:
	$(GO) vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	test -z "$(shell gofmt -l .)"

clean:
	rm -rf dist

deps:
	@which goreleaser >/dev/null || { \
		echo "ERROR: goreleaser not installed. See https://goreleaser.com/install/"; \
		exit 1; \
	}

install: build
	sudo install -m 755 dist/hostit /usr/bin/hostit

install-deb: purge-package
	sudo dpkg -i dist/hostit-*_linux_amd64.deb

purge-package:
	sudo apt-get purge hostit hostit-control hostit-node hostit-proxy || true
