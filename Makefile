VERSION ?= 0.1.0
GO      ?= go

.PHONY: help build deb deb-arm64 release release-snapshot check test vet fmt fmt-check clean deps install install-deb purge-package

help:
	@echo "Build:"
	@echo "  make build              - Build a local dev binary to dist/hostit"
	@echo "  make deb                - Build dist/hostit_$(VERSION)_linux_amd64.deb (dpkg-deb, no git needed)"
	@echo "  make deb-arm64          - Build dist/hostit_$(VERSION)_linux_arm64.deb"
	@echo
	@echo "Test/check:"
	@echo "  make check              - Run tests, formatting checks and vetting"
	@echo "  make test               - Run tests"
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

deb:
	scripts/mkdeb.sh $(VERSION) amd64

deb-arm64:
	scripts/mkdeb.sh $(VERSION) arm64

release: clean deps check
	goreleaser release --clean

release-snapshot: clean deps check
	goreleaser release --snapshot --clean

check: test fmt-check vet

test:
	$(GO) test ./...

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
