SHELL = bash
PROJECT_ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

GO_TEST_CMD = go test
GO_TEST_PKGS ?= ./...

default: help

ifeq ($(CI),true)
$(info Running in a CI environment, verbose mode is disabled)
else
VERBOSE="true"
endif

# include per-user customization after all variables are defined
-include Makefile.local

HELP_FORMAT="    \033[36m%-20s\033[0m %s\n"
.PHONY: help
help: ## Display this usage information
	@echo "Valid targets:"
	@{ \
		echo $(MAKEFILE_LIST) \
			| xargs grep -E '^[^ \$$]+:.*?## .*$$' -h \
			; \
		echo $(MAKEFILE_LIST) \
			| xargs cat 2> /dev/null \
			| sed -e 's/$\(eval/$\(info/' \
			| make -f- 2> /dev/null \
			| grep -E '^[^ ]+:.*?## .*$$' -h \
			; \
	} \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; \
			{printf $(HELP_FORMAT), $$1, $$2}'
	@echo ""

.PHONY: tidy
tidy: ## Tidy Go modules
	go mod tidy

.PHONY: test
test: ## Run the test suite
	$(if $(ENABLE_RACE),GORACE="strip_path_prefix=$(GOPATH)/src" )$(GO_TEST_CMD) \
		$(if $(ENABLE_RACE),-race) $(if $(VERBOSE),-v) \
		-cover \
		-coverprofile=unit.coverprofile \
		$(if $(ENABLE_RACE),-covermode=atomic,-covermode=count) \
		-timeout=15m \
		$(GO_TEST_PKGS)

unit.coverprofile: # rule to ensure unit.coverprofile exists
	@if [ ! -f $@ ]; then \
		echo "No coverage file found. Running tests to generate coverage data..."; \
		$(MAKE) test; \
	fi

.PHONY: coverage
coverage: unit.coverprofile
coverage: ## Open a web browser displaying coverage
	go tool cover -html=$<

.PHONY: coverage-total
coverage-total: unit.coverprofile
coverage-total: ## Print total coverage percentage
	@go tool cover -func $< | grep total | awk '{ printf "total coverage: %s of statements\n", $$3 }'

.PHONY: clean
clean: ## Remove build artifacts
	@rm -f $(if $(VERBOSE),-v) *.out coverage.* *.coverprofile profile.cov
