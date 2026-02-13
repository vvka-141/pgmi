# pgmi Documentation

## Getting Started

- **[QUICKSTART.md](QUICKSTART.md)** — Your first deployment in 10 minutes (install, configure, deploy, verify)
- **[WHY-PGMI.md](WHY-PGMI.md)** — When pgmi's approach makes sense (and when it doesn't)
- **[COMING-FROM.md](COMING-FROM.md)** — Migration guides from Flyway, Liquibase, and raw psql

## Reference

- **[CLI.md](CLI.md)** — Complete CLI reference (all commands, flags, exit codes, examples)
- **[CONFIGURATION.md](CONFIGURATION.md)** — pgmi.yaml schema and precedence rules
- **[session-api.md](session-api.md)** — Session tables (`pgmi_source_view`, `pgmi_plan_view`) and helper functions

## AI Assistant Integration

pgmi embeds AI-digestible documentation directly in the binary:

```bash
pgmi ai                    # Overview for AI assistants
pgmi ai skills             # List embedded skills
pgmi ai skill pgmi-sql     # Load SQL conventions
```

See [CLI.md#pgmi-ai](CLI.md#pgmi-ai) for complete reference.

## Features

- **[TESTING.md](TESTING.md)** — Database testing with automatic rollback (fixtures, hierarchical setup, gated deployments)
- **[METADATA.md](METADATA.md)** — Optional script tracking with UUIDs, idempotency flags, and sort keys
- **[SECURITY.md](SECURITY.md)** — Secrets handling, threat model, and CI/CD pipeline patterns
- **[MCP.md](MCP.md)** — Model Context Protocol integration for AI assistants (tools, resources, prompts, HTTP gateway)

## Operations

- **[PRODUCTION.md](PRODUCTION.md)** — Performance, rollback strategies, lock management, monitoring
