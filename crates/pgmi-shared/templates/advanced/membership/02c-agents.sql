/*
<pgmi-meta
    id="5c9867d0-b33c-4d16-a80f-f1014ad59fcc"
    idempotent="false">
  <description>
    Membership structure, part 5: actor kinds. The audit log records the
    actor kind of every change: human, agent (an MCP client through its
    connection, membership.agent_principal), service or system. Rows
    written before actor kinds existed carry none, so the NOT NULL
    constraint holds for new rows only.
  </description>
  <sortKeys>
    <key>005/000/002/003</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE membership.audit_event
    ADD COLUMN actor_kind text CONSTRAINT audit_event_actor_kind_check CHECK (actor_kind IN ('human', 'agent', 'service', 'system')),
    ADD CONSTRAINT audit_event_actor_kind_not_null NOT NULL actor_kind NOT VALID;

COMMENT ON COLUMN membership.audit_event.actor_kind IS
    'What really acted: human (a person signed in), agent (an MCP client through its connection), service (a machine actor), system (no principal: the deploy, a sign-in). NULL only on rows written before actor kinds were recorded.';
