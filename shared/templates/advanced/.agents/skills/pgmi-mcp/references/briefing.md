# The answer is a briefing

What a tool answers, how it refuses and how the model recovers. One shape in
every tool, so the model learns it once.

## Contents

1. The shape
2. Rich is not big
3. Next actions the caller may take
4. Refusals: code, rule, remedy
5. A name that matched nothing
6. Writes: an array, all or nothing
7. Prompts and resources

## 1. The shape

`structuredContent`, declared in `outputSchema`:

| Key | Holds | When |
|---|---|---|
| `outcome` | `code` (`found`, `no_match`, `done`, `refused`), the `id` the call is about, the record | a call about one record or one operation |
| the rows, under one named key (`tasks`) | the page | a list |
| `context` | the few facts beside the record that change the next decision | when there are any |
| `hints` | `[{code, severity, message}]`, the server's own sentences | always, `[]` for none |
| `nextActions` | `[{tool, arguments}]`, arguments filled from this answer | always, `[]` for none |
| `total`, `hasMore`, `omitted` | how much there is, and why rows were left out (`{reason, count}`) | a list |
| `asOf` | when it was read | always |
| `forPerson` | one sentence the agent relays to its person as it is | only when a change waits for the person |

A key with nothing in it is left out, except the arrays the schema
requires. Build the shape in one shared helper once a second tool needs it,
and declare the keys once in shared `$defs` of each schema.

## 2. Rich is not big

For each candidate fact ask: would the agent act differently knowing it?
Keep it only then.

- **Derived, not raw**: answer `isOverdue` and `daysLeft`, not two dates to
  subtract. A rule the screen shows too is one function or view column both
  read (pgmi-layers).
- **Rare facts wait for `detail=full`**, and `omitted` says what was left out
  at `summary`.
- **Presence, not content**, for what the caller may not read: a section
  behind a further permission is absent and named in `omitted`.
- **Never count what row-level security hides**, not even in `omitted`.

## 3. Next actions the caller may take

- Only calls the caller may make. A tool is callable when it is listed for
  the caller: `EXISTS (SELECT FROM jsonb_array_elements(api.mcp_list_tools()->'tools') t WHERE t->>'name' = 'close_task')`,
  the same decision the call will take. A next action is a hint, not a grant.
- Arguments are ids, dates and enum words from this answer, never stored
  text and never a value the agent would have to edit.
- Reads, or the task's own next step; never a decision the person keeps
  (approve, pay, send).
- In the order the agent should take them.

## 4. Refusals: code, rule, remedy

A refusal answers `isError: true` with one text item, a JSON object:

| Member | Holds |
|---|---|
| `code` | a stable machine word, a literal at the call |
| `rule` | what refused, in the server's words |
| `remedy` | the call that would succeed |
| `invalidParams` | every refused argument at once, `[{name, reason}]`, ordered |

Compute the refusals as `(field, reason)` rows, the entity's
`core.fn_<entity>_problems` function where one exists, so a REST handler and
the tool refuse alike. Not found, removed and hidden by row-level security
are one refusal, never told apart. Once several tools share codes, keep
`rule` and `remedy` in one catalog keyed by code (a table synced from
`data/catalogs/`), and a test that fails for a code without its row.

## 5. A name that matched nothing

An agent searches by what the person said. When a name matches nothing,
answer the search, not an error: `outcome.code` `no_match`, a hint, and up
to three candidates the caller may see, best first, each with a next action
by id:

```sql
SELECT t.object_id AS id, t.title, extensions.similarity(t.title, v_name) AS similarity
FROM core.task t
WHERE extensions.similarity(t.title, v_name) > 0.2
ORDER BY 3 DESC, t.object_id
LIMIT 3;
```

The query reads through row-level security, so it never suggests another
organization's task. It runs only on a miss. An id that names nothing is a
refusal (section 4): ids come from earlier answers.

## 6. Writes: an array, all or nothing

- A write tool takes its items as an array capped by `maxItems`, which the
  framework enforces before the body runs; one change is an array of one.
- Probe every item's problems, and the array as a set (the same record
  twice, two items with one unique name), before writing anything. Any item
  refused: write nothing, answer `refused` with an entry per item by `index`.
- Otherwise call the same kernels the REST handlers call, in order, and
  answer `done` with each item's kernel result, never `{"ok": true}`.
- If a kernel still refuses after earlier items were written (a concurrent
  change), answer `refused` like any other refusal. An `isError` answer
  changes nothing: the framework rolls back every write the call made.
- Declare `effect` `write`, and `reversible` false when the change cannot be
  undone; keep reads and writes in separate tools.

## 7. Prompts and resources

Guidance an agent needs before it works is served from the database, not
shipped as files: a resource (`uriTemplate`, `mimeType`) answers JSON read
at request time; a prompt (`arguments`) answers the server's literal text
naming the tools to use and what never to do. Both declare access and are
tested through `api.mcp_read_resource` / `api.mcp_get_prompt`.
