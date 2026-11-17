# pgmi website

Requires Hugo Extended 0.163.3, and Go because Hugo fetches the theme as a Go
module (`website/go.mod`). The GitHub Pages workflow pins the same Hugo version.

Preview locally:

```bash
hugo server --source website --disableFastRender
```

Production build:

```bash
hugo --source website --minify --gc
```

The site mounts the repository `docs/` directory into `/docs/`, so edit published documentation in the repository root `docs/` tree.

## Article URLs are stable

An article's URL is `/articles/<file name>/`. To rename or move an article,
add its old path to the article's front matter so old links keep working:

```yaml
aliases: [/articles/old-file-name/]
```

Link to the article, not to a heading, unless the heading is stable. The
docs workflow's link check reaches article URLs only on the published site
and does not check headings, so a renamed one breaks such a link silently.
