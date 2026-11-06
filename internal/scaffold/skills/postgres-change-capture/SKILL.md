---
name: postgres-change-capture
description: Index of PostgreSQL recipes for recording and reacting to data changes - an audit table filled by triggers (old and new rows as jsonb, actor, transition tables), publications and logical replication slots for an external change feed or CDC, and LISTEN/NOTIFY with pg_notify for in-app signals. Use when asked to audit or log who changed a row, keep a change history, stream changes to another system, or wake a worker when a row arrives.
---

# PostgreSQL change-capture recipes

If this project's README has a section on change tracking, follow it first.

| Facing | Read |
|---|---|
| "Record who changed which row, when, from what" | [audit trigger](references/audit-trigger.md) |
| "Another system must receive every change, in order" | [logical decoding](references/logical-decoding.md) |
| "Wake a worker the moment a row arrives" | [LISTEN/NOTIFY](references/listen-notify.md) |

Only the audit trigger is transactional with the change and queryable in this
database. A publication is durable but leaves the database; a notification is
neither durable nor queryable, so it always points at a table.
