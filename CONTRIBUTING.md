# Contributing to pgmi

Thank you for your interest in contributing to pgmi! This document provides guidelines and standards for contributing to the project.

## Code of Conduct

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md). Please report unacceptable behavior to the maintainers.

## Reporting Issues

### Bug Reports

Before creating a bug report, please check existing issues. When reporting, include:

- **Clear title** describing the issue
- **Steps to reproduce** the behavior
- **Expected vs. actual** behavior
- **Environment**: pgmi version (`pgmi --version`), PostgreSQL version, OS
- **Relevant logs** or error messages

### Feature Requests

Feature requests are welcome! Please:

- Check existing issues for similar requests
- Describe the use case and problem you're solving
- Explain how it aligns with pgmi's philosophy (execution fabric, not migration framework)

### Security Issues

For security vulnerabilities, please see [SECURITY.md](SECURITY.md). Do not open public issues for security problems.

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
- Integration tests should use Docker Compose or test fixtures

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

This project follows gitflow:

- `main` - stable production releases
- `develop` - integration branch for features
- `feature/*` - feature branches
- `hotfix/*` - urgent production fixes

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

1. Create a feature branch from `develop`
2. Implement your changes with tests
3. Run `go test ./...` to verify all tests pass
4. Run `go build ./...` to ensure everything compiles
5. Update documentation if needed
6. Submit PR with clear description of changes

## Questions?

- Open a [GitHub Discussion](https://github.com/vvka-141/pgmi/discussions) for questions
- Check existing issues and discussions first
- For security issues, see [SECURITY.md](SECURITY.md)

## License

By contributing to pgmi, you agree that your contributions will be licensed under:
- **MPL-2.0** for tool code
- **MIT** for template code (in `internal/scaffold/templates/`)
