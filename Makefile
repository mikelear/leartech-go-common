# leartech-go-common is a LIBRARY: no binary, no chart, no swagger. So this
# Makefile is only the local half of CI — the same golden mk the pipeline
# curls, driven with the same settings, so a laptop reproduces CI.
#
# Before this there was no Makefile at all. Every gate this repo has
# (go-lint, go-test with a coverage floor, govulncheck, comment-gate) existed
# only inside .lighthouse, so the first time anyone saw a failure was on a PR.

LEARTECH_GO_MK_REF ?= main
LEARTECH_GO_MK_URL ?= https://raw.githubusercontent.com/mikelear/leartech-pipeline-catalog/$(LEARTECH_GO_MK_REF)/go/leartech-go.mk
LEARTECH_GO_MK     := .leartech-go.mk

# THESE MUST MATCH .lighthouse/jenkins-x/test.yaml.
#
# The golden mk defaults to COVERAGE_SCOPE=./internal/... and this repo has no
# internal/. Verified 2026-09-22 by running it: the default scope reports
# total=0.0% and FAILS the floor — loudly, not silently. So the override is
# what makes the local gate measure this repo's code and agree with CI
# (86.1% against a floor of 85.0), not what stops a false pass.
COVERAGE_SCOPE     ?= ./pkg/...
COVERAGE_THRESHOLD ?= 85.0

MK = $(MAKE) SHELL=/bin/bash -f $(LEARTECH_GO_MK) \
     COVERAGE_SCOPE=$(COVERAGE_SCOPE) COVERAGE_THRESHOLD=$(COVERAGE_THRESHOLD)

.DEFAULT_GOAL := help
.PHONY: help list fetch-mk refresh-mk lint test test-coverage vuln pre-push require-committed comment-gate verify

help: ## Show the targets
	@grep -h -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

list: ## List every target
	@grep -oE '^[a-zA-Z0-9_-]+:' $(MAKEFILE_LIST) | tr -d ':' | sort -u

fetch-mk: $(LEARTECH_GO_MK) ## Fetch the golden go/leartech-go.mk from pipeline-catalog

$(LEARTECH_GO_MK):
	@echo "==> fetching $(LEARTECH_GO_MK_URL)"
	@curl -fsSL -o $@ $(LEARTECH_GO_MK_URL)

# refresh-mk FIRST in verify, so the gate run locally is the gate CI will run.
# Without it a local pass is against however old the cached copy happens to be.
refresh-mk: ## Discard the cached golden mk so the next fetch is a real one
	@rm -f $(LEARTECH_GO_MK)

# SHELL=/bin/bash: the golden mk uses bash-only syntax. CI images ship bash as
# /bin/sh so the drift is invisible there; a laptop /bin/sh needs the override.
lint: fetch-mk ## golangci-lint against the merged estate config
	$(MK) lint

test: fetch-mk ## Unit tests
	$(MK) test

test-coverage: fetch-mk ## Race + coverage against the floor CI enforces
	$(MK) test-coverage

vuln: fetch-mk ## govulncheck
	$(MK) vuln

pre-push: fetch-mk ## vet tidy-check build test-coverage lint vuln
	$(MK) pre-push

# require-committed exists because comment-gate diffs COMMITTED work. Run
# against a dirty tree it reports +0/+0 and passes without reading anything —
# a check that looked at nothing has to say so loudly rather than pass.
require-committed:
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "FAIL: uncommitted changes. comment-gate diffs COMMITTED work, so"; \
		echo "  running it now would report +0/+0 and pass without reading your"; \
		echo "  changes. Commit first, then re-run."; \
		git status --short | sed 's/^/    /'; \
		exit 1; \
	fi

comment-gate: require-committed fetch-mk ## Challenge added prose: ratchet + claims must name a proof
	$(MK) comment-gate \
		COMMENTGATE_BASE=$(if $(PULL_BASE_REF),origin/$(PULL_BASE_REF),origin/main)

# verify is THE local entry point. pre-push alone is not: CI also runs
# comment-gate, which pre-push does not reach.
verify: refresh-mk pre-push comment-gate ## Everything CI gates on, before pushing
	@echo "==> verify: pre-push + comment-gate all passed"
