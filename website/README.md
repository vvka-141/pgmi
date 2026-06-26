# pgmi website

Requires Hugo Extended 0.163.3 and Go. The GitHub Pages workflow pins the same Hugo version.

Preview locally:

```bash
hugo server --source website --disableFastRender
```

Production build:

```bash
hugo --source website --minify --gc
```

The site mounts the repository `docs/` directory into `/docs/`, so edit published documentation in the repository root `docs/` tree.
