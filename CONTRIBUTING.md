# Contributing to pgmi

Thank you for your interest in contributing to pgmi. This document covers the
checks and conventions needed to prepare a change for review.

## Code of Conduct

By participating in this project, you agree to abide by our
[Code of Conduct](CODE_OF_CONDUCT.md). Report conduct incidents privately through
the [private reporting channel](https://github.com/vvka-141/pgmi/security/advisories/new)
and prefix the title with `Code of Conduct`.

## Reporting Issues

### Bug Reports

Before creating a bug report, check existing issues, then use the
[bug report form](https://github.com/vvka-141/pgmi/issues/new?template=bug.yml).
Include:

- **Clear title** describing the issue
- **Steps to reproduce** the behavior
- **Expected vs. actual** behavior
- **Environment**: pgmi version (`pgmi --version`), PostgreSQL version, OS
- **Relevant logs** or error messages

### Feature Requests

Use the
[feature request form](https://github.com/vvka-141/pgmi/issues/new?template=feature.yml)
and:

- Check existing issues for similar requests
- Describe the use case and problem you're solving
- Explain how it aligns with pgmi's philosophy (execution fabric, not migration framework)

### Security Issues

For security vulnerabilities, please see [SECURITY.md](SECURITY.md). Do not open public issues for security problems.

## Development setup

You need:

- Go 1.25.12 or the version declared by `go.mod`
- Docker, or `PGMI_TEST_CONN` pointing to a PostgreSQL test database, for
  integration tests
- golangci-lint 1.64.8 for `make lint`

Start with:

```bash
make doctor
make test
make build
```

`make test` runs the short unit suite and does not require PostgreSQL.
`make test-integration` runs the full suite and starts a PostgreSQL
Testcontainer when `PGMI_TEST_CONN` is not set.

## Code Style

### Error Wrapping

**Always use context-first error wrapping** with `fmt.Errorf`:

```go
// ✅ Good: Context first, then %w
return fmt.Errorf("failed to connect to database: %w", err)
return fmt.Errorf("invalid configuration: %w", ErrInvalidConfig)

// ❌ Bad: Error first
return fmt.Errorf("%w: failed to connect", err)
return fmt.Errorf("%w: invalid configuration", ErrInvalidConfig)
```

**Rationale**: Context-first makes error messages more readable in logs and stack traces. The error chain naturally flows from general to specific when errors bubble up.

### Naming Conventions

- **Constants**: Use `PascalCase` for exported constants, `camelCase` for unexported
- **Interfaces**: Use descriptive names ending in `-er` when appropriate (`Connector`, `Deployer`, `Approver`)
- **Test Files**: Use `_test.go` suffix for unit tests, `_integration_test.go` for integration tests

### Dependency Injection

All services use constructor injection with explicit dependencies:

```go
func NewDeploymentService(
    connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error),
    approver pgmi.Approver,
    logger pgmi.Logger,
    // ... other dependencies
) *DeploymentService {
    // Validate all dependencies are non-nil
    if connectorFactory == nil {
        panic("connectorFactory cannot be nil")
    }
    // ...
}
```

**Panic on nil dependencies**: Constructor panic is acceptable for programmer errors (misconfigured DI). Document this behavior in godoc.

### Testing

- Write table-driven tests using `[]struct` pattern
- Use descriptive test names: `TestFunctionName_Scenario`
- Prefer in-memory implementations for external dependencies (filesystem, database) when possible
- Integration tests should use the repository's Testcontainers helpers or
  `PGMI_TEST_CONN`

Example:

```go
func TestParser_ParseConnectionString(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    ConnectionConfig
        wantErr bool
    }{
        {
            name: "valid PostgreSQL URI",
            input: "postgresql://user:pass@localhost:5432/mydb",
            want: ConnectionConfig{
                Host: "localhost",
                Port: 5432,
                Username: "user",
                Password: "pass",
                Database: "mydb",
            },
            wantErr: false,
        },
        // ... more test cases
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Parse(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("Parse() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

## Git Workflow

This project uses a simplified gitflow:

- `main` - stable production releases (tagged with `vX.Y.Z`)
- `feature/*` - feature branches (created from and merged to `main`)
- `hotfix/*` - urgent production fixes

**Workflow:**
1. Create feature branch from `main`: `git checkout -b feature/my-feature`
2. Implement changes with tests
3. Push and create PR to `main`
4. After merge, maintainers create a semantic version tag. See
   [Release Guide](RELEASES.md).

## Commit Messages

Follow conventional commits format:

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`

Examples:
- `feat(cli): add --timeout flag to deploy command`
- `fix(resolver): handle empty PGPORT environment variable`
- `refactor(services): extract SessionManager from DeploymentService`
- `test(resolver): add connection resolution test cases`

## Pull Requests

1. Create a feature branch from `main`
2. Implement your changes with tests
3. Run `make test` and `make lint`
4. Run `make test-integration` (requires Docker or `PGMI_TEST_CONN`)
5. Run `make build`
6. If the change affects scaffolding templates, embedded SQL, or deploy/test
   execution, run `make build-clean` and
   `go test ./internal/scaffold -v -run TestTemplateDeployment -timeout 5m`
7. Update documentation in the same change as the behavior it describes
8. Submit a PR to `main` with a clear description and verification notes

On Windows without `make`, the core equivalents are:

```powershell
go test -short -tags pgmi_testhooks ./...
go test -tags pgmi_testhooks ./...
golangci-lint run
go build -o pgmi.exe ./cmd/pgmi
```

Set `PGMI_REQUIRE_DB=1` for the full integration run when a missing database
must fail rather than skip.

## Questions?

- Read [Support](SUPPORT.md), then use the
  [question form](https://github.com/vvka-141/pgmi/issues/new?template=question.yml)
- Check existing issues first
- For security issues, see [SECURITY.md](SECURITY.md)

## License

By contributing to pgmi, you agree that your contributions will be licensed under:
- **MPL-2.0** for tool code
- **MIT** for template code (in `internal/scaffold/templates/`)
