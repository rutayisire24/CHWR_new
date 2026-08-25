# National Community Health Worker Registry.
#
# `make` on its own prints the targets. Every variable below can be overridden
# on the command line: `make run ADDR=:8099 DATABASE_URL=postgres:///other`.

# The server reads plain environment variables; these are the development
# defaults, matching .env.example and internal/config.
DATABASE_URL ?= postgres:///chwr
ADDR         ?= :8080
ENV          ?= dev

# The port on its own, so a health check works whether ADDR is ":8080" or
# "0.0.0.0:8080".
PORT := $(lastword $(subst :, ,$(ADDR)))

BINARY  := chwr-server
RUN_DIR := .run
PID     := $(RUN_DIR)/server.pid
LOG     := $(RUN_DIR)/server.log

# Every recipe that talks to the database or the server takes the same
# environment, so a target cannot quietly use a different one.
ENVIRONMENT := DATABASE_URL="$(DATABASE_URL)" ADDR="$(ADDR)" ENV="$(ENV)"

.DEFAULT_GOAL := help
# `seed` chains migrate, hierarchy, facilities and verify, and each depends on
# the one before it having finished. -j must not reorder them.
.NOTPARALLEL:
.PHONY: help build run start stop restart logs status test vet fmt check migrate \
        seed seed-hierarchy seed-facilities verify admin dist clean

help: ## Print this list
	@echo "National Community Health Worker Registry"
	@echo
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "  DATABASE_URL=$(DATABASE_URL)"
	@echo "  ADDR=$(ADDR)   ENV=$(ENV)"
	@echo
	@echo "  Override any of them: make restart ADDR=:8099"

## ---------------------------------------------------------------- building

build: ## Compile the server to ./chwr-server
	go build -o $(BINARY) ./cmd/server

dist: ## Build a static linux/amd64 binary for deployment
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o $(BINARY)-linux-amd64 ./cmd/server
	@ls -lh $(BINARY)-linux-amd64 | awk '{print "  built " $$9 ", " $$5}'

## ----------------------------------------------------------------- running

run: build ## Build and run in the foreground (Ctrl-C to stop)
	$(ENVIRONMENT) ./$(BINARY)

start: build ## Build and start in the background
	@mkdir -p $(RUN_DIR)
	@if [ -f $(PID) ] && kill -0 `cat $(PID)` 2>/dev/null; then \
	    echo "  already running as PID `cat $(PID)` — use 'make restart'"; exit 1; \
	fi
	@$(ENVIRONMENT) ./$(BINARY) >> $(LOG) 2>&1 & echo $$! > $(PID)
	@sleep 1
	@if kill -0 `cat $(PID)` 2>/dev/null; then \
	    echo "  started PID `cat $(PID)` on $(ADDR), logging to $(LOG)"; \
	else \
	    rm -f $(PID); echo "  failed to start — last lines of $(LOG):"; tail -5 $(LOG); exit 1; \
	fi

stop: ## Stop the background server
	@if [ -f $(PID) ] && kill -0 `cat $(PID)` 2>/dev/null; then \
	    kill `cat $(PID)`; rm -f $(PID); echo "  stopped"; \
	else \
	    rm -f $(PID); echo "  not running"; \
	fi

restart: ## Rebuild and restart the background server
	@$(MAKE) --no-print-directory stop
	@$(MAKE) --no-print-directory start

status: ## Is it running, and is it answering
	@if [ -f $(PID) ] && kill -0 `cat $(PID)` 2>/dev/null; then \
	    echo "  running as PID `cat $(PID)` on $(ADDR)"; \
	else \
	    echo "  not running"; \
	fi
	@code=`curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
	    "http://127.0.0.1:$(PORT)/healthz" 2>/dev/null`; \
	 case "$$code" in \
	   200) echo "  /healthz -> 200, the pool is up";; \
	   000|"") echo "  /healthz -> unreachable";; \
	   *) echo "  /healthz -> $$code";; \
	 esac

logs: ## Follow the background server's log
	@touch $(LOG); tail -f $(LOG)

## ----------------------------------------------------------------- checking

test: ## Run the test suite
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format every package
	gofmt -w cmd internal

check: fmt vet test ## Format, vet and test — run before committing

## ------------------------------------------------------------ the database

migrate: build ## Apply migrations and exit
	$(ENVIRONMENT) ./$(BINARY) -migrate

seed-hierarchy: ## Extract and load the 84,635 administrative units (~3s)
	python3 seed/extract_units.py
	psql -d "$(DATABASE_URL)" -f seed/load_hierarchy.sql

seed-facilities: ## Extract and load the Master Facility List
	python3 seed/extract_facilities.py
	psql -d "$(DATABASE_URL)" -f seed/load_facilities.sql

seed: migrate seed-hierarchy seed-facilities verify ## Migrate, load the hierarchy and facilities, then verify

verify: ## Probe the schema with the bad-data suite — 39 cases, all must say blocked
	psql -d "$(DATABASE_URL)" -f seed/verify_constraints.sql

admin: build ## Provision a national admin: make admin EMAIL=you@example.org NAME="Your Name"
	@test -n "$(EMAIL)" || { echo "  EMAIL is required: make admin EMAIL=you@example.org NAME=\"Your Name\""; exit 1; }
	$(ENVIRONMENT) ./$(BINARY) -create-admin "$(EMAIL)" -name "$(NAME)"

clean: ## Remove built binaries and run state
	rm -f $(BINARY) $(BINARY)-linux-amd64
	rm -rf $(RUN_DIR)
