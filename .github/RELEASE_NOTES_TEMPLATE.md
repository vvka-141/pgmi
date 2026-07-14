# Release-notes template

A GitHub release page is a **public surface strangers reach from search and
release feeds**. Most of them have never heard of pgmi. Write for them.

Before tagging, add a section to `RELEASES.md` following the shape below. CI
lifts that section verbatim to the top of the GitHub release; the commit log is
collapsed underneath it. If the section is missing, **the release build fails** —
that is deliberate. The narrative is not optional paperwork; it is the release.

---

## The shape

```markdown
## vX.Y.Z — YYYY-MM-DD

<One sentence: who benefits and what they can now do that they could not before.>

<Two or three sentences of context. What problem does this solve? What is the
new workflow? If nothing here would matter to a stranger, say so plainly —
"a maintenance release, no user-visible change" is a fine and honest release.>

### What you can do now

<The headline capability, as a workflow rather than a feature list. Show it.>

```bash
pgmi deploy ./project -d mydb --json | jq .exitCode
```

### Upgrading

<Anything that can break. "No action needed" if nothing can — but say it, do not
leave the reader guessing. Name the session API contract version if it moved.>

### Also in this release

- <Short, outcome-shaped bullets. What changed for the user, not what was edited.>
- <Bugs: what was broken, and what it did to you. Not the diff.>
```

---

## Rules

1. **Lead with the person, not the change.** "You can now deploy the advanced
   template to RDS" beats "Removed the DDL event trigger".
2. **A stranger must understand it in 30 seconds.** They do not know what a
   `pgmi-meta` block is, and they will not read to find out.
3. **Ticket IDs are not content.** They belong in the collapsed commit log,
   which CI generates for you. Do not paste `(PGMI-142)` into the narrative.
4. **Show, don't enumerate.** One runnable example is worth ten bullets.
5. **Upgrade risk is the second thing a returning user looks for.** Never omit
   it. "No action needed" is an answer; silence is not.
6. **File counts, commit counts, and line-diff totals are ledger noise.** They
   measure effort, not value, and the reader is not buying effort.
7. **Be honest about a thin release.** A maintenance release that says so earns
   more trust than one padded to look substantial.

## What CI does with it

* `scripts/release-notes.sh <tag>` extracts the section for that tag.
* `.github/workflows/release.yml` passes it to GoReleaser as `RELEASE_SUMMARY`.
* `.goreleaser.yaml` renders it **above** the install instructions, then collapses
  the auto-generated commit log into a `<details>` block below.
* No section for the tag → the release job fails before publishing anything.
