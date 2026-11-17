/*
<pgmi-meta
    id="74bb46a5-588d-4af7-989d-9ba8810ad591"
    idempotent="false">
  <description>
    Change tracking, part 2: each change records its actor kind
    (api.current_actor_kind()) beside the effective and actual principals.
    Rows written before carry none.
  </description>
  <sortKeys>
    <key>005/000/100/001</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE core.change_event
    ADD COLUMN actor_kind text CONSTRAINT change_event_actor_kind_check CHECK (actor_kind IN ('human', 'agent', 'service', 'system')),
    ADD CONSTRAINT change_event_actor_kind_not_null NOT NULL actor_kind NOT VALID;

COMMENT ON COLUMN core.change_event.actual_principal_id IS
    'api.current_actual_principal_id() of the change: who really made it, an agent when it came through an approved connection.';
COMMENT ON COLUMN core.change_event.actor_kind IS
    'api.current_actor_kind() of the change: human, agent, service, or system for the deploy and other contexts naming no one. NULL only on rows written before actor kinds were recorded.';
