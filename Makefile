VERSION ?= 0.1.0
GO      ?= go
NPM     ?= npm

.PHONY: help build web web-deps web-build deb deb-arm64 release release-snapshot check test e2e vet fmt fmt-check clean deps install install-deb purge-package

help:
	@echo "Build:"
	@echo "  make web                - Build the React web app into server/site (needed before build)"
	@echo "  make build              - Build a local dev binary to dist/hostit"
	@echo "  make deb                - Build dist/hostit_$(VERSION)_linux_amd64.deb (dpkg-deb, no git needed)"
	@echo "  make deb-arm64          - Build dist/hostit_$(VERSION)_linux_arm64.deb"
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

# The built app is embedded via server/site (see server/web.go); .gitignore keeps
# the generated assets out of git, but index.html stays as a placeholder
web-build:
	cd web && $(NPM) run build
	rm -rf server/site
	mkdir -p server/site
	cp -r web/build/. server/site/

deb:
	scripts/mkdeb.sh $(VERSION) amd64

deb-arm64:
	scripts/mkdeb.sh $(VERSION) arm64

release: clean deps web check
	goreleaser release --clean

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
e2e:
	@test -n "$(HOSTIT_HOST)" || { echo "set HOSTIT_HOST and HOSTIT_TOKEN"; exit 1; }
	$(GO) test -tags e2e -count 1 -timeout 30m -run '$(RUN)' -v ./e2e/

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
	sudo dpkg -i dist/hostit_*_linux_amd64.deb

purge-package:
	sudo apt-get purge hostit || true
