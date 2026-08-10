NAME=kubevirt
BINARY=packer-plugin-${NAME}

COUNT?=1
TEST?=$(shell go list ./...)
HASHICORP_PACKER_PLUGIN_SDK_VERSION?=$(shell go list -m github.com/hashicorp/packer-plugin-sdk | cut -d " " -f2)
PLUGIN_FQN=$(shell grep -E '^module' <go.mod | sed -E 's/module \s*//')
PLUGIN_SRC=$(shell echo "${PLUGIN_FQN}" | sed 's/packer-plugin-//')

.PHONY: dev

build:
	@go build -o ${BINARY}

dev:
	go build -ldflags="-X '${PLUGIN_FQN}/version.VersionPrerelease=dev'" -o ${BINARY}
	packer plugins install --path ${BINARY} "${PLUGIN_SRC}"
	# macOS intermittently SIGKILLs the freshly installed dev plugin when Packer
	# execs it to `describe`, so Packer reports it as missing. Re-signing the
	# installed binary in place clears that; the checksum sidecar is rewritten so
	# `packer` still accepts the (now re-signed) binary.
	@if [ "$$(uname -s)" = "Darwin" ]; then \
		installed=$$(ls "$${PACKER_PLUGIN_PATH:-$$HOME/.config/packer/plugins}/${PLUGIN_SRC}/${BINARY}_"* 2>/dev/null | grep -v '_SHA256SUM$$' | head -1); \
		if [ -n "$$installed" ]; then \
			codesign --force --sign - "$$installed"; \
			shasum -a 256 "$$installed" | awk '{ printf "%s", $$1 }' > "$${installed}_SHA256SUM"; \
			echo "Re-signed installed plugin and updated checksum: $$installed"; \
		else \
			echo "WARNING: could not find installed plugin to re-sign under $${PACKER_PLUGIN_PATH:-$$HOME/.config/packer/plugins}/${PLUGIN_SRC}/"; \
		fi; \
	fi

test:
	@go test -race -count $(COUNT) $(TEST) -timeout=3m

install-packer-sdc: ## Install packer sofware development command
	@go install github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc@${HASHICORP_PACKER_PLUGIN_SDK_VERSION}

plugin-check: install-packer-sdc build
	@packer-sdc plugin-check ${BINARY}

testacc: dev
	@PACKER_ACC=1 go test -count $(COUNT) -v $(TEST) -timeout=120m

generate: install-packer-sdc
	@go generate ./...
	@rm -rf .docs
	@packer-sdc renderdocs -src docs -partials docs-partials/ -dst .docs/
	@./.web-docs/scripts/compile-to-webdocs.sh "." ".docs" ".web-docs" "hashicorp"
	@rm -r ".docs"
