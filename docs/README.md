# pgmi Documentation

## Getting Started

- **[QUICKSTART.md](QUICKSTART.md)** — Your first deployment in 10 minutes (install, configure, deploy, verify)
- **[WHY-PGMI.md](WHY-PGMI.md)** — When pgmi's approach makes sense (and when it doesn't)
- **[COMING-FROM.md](COMING-FROM.md)** — Migration guides from Flyway, Liquibase, and raw psql

## Reference

- **[CLI.md](CLI.md)** — Complete CLI reference (all commands, flags, exit codes, examples)
- **[CONFIGURATION.md](CONFIGURATION.md)** — pgmi.yaml schema and precedence rules
- **[session-api.md](session-api.md)** — Session tables (`pgmi_source`, `pgmi_plan`) and helper functions

## Features

- **[TESTING.md](TESTING.md)** — Database testing with automatic rollback (fixtures, hierarchical setup, gated deployments)
- **[METADATA.md](METADATA.md)** — Optional script tracking with UUIDs, idempotency flags, and sort keys
- **[SECURITY.md](SECURITY.md)** — Secrets handling, threat model, and CI/CD pipeline patterns

## Operations

- **[PRODUCTION.md](PRODUCTION.md)** — Performance, rollback strategies, lock management, monitoring
- **[retry-timeout-behavior.md](retry-timeout-behavior.md)** — Timeout configuration and retry mechanics

## Archive

The `archive/` directory contains historical development notes and design decisions.
