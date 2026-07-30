## Summary

Describe the problem and the smallest change that solves it.

## Verification

List the commands or manual checks you ran.

## Checklist

- [ ] Tests cover the changed behavior.
- [ ] `make test` passes.
- [ ] `make lint` passes.
- [ ] Documentation changed with the behavior, or no documentation change is needed.
- [ ] CLI changes remain infrastructure-only; deployment orchestration stays in `deploy.sql`.
- [ ] SQL/template execution changes were verified with a live PostgreSQL deployment.
- [ ] No credentials or generated build artifacts are included.
