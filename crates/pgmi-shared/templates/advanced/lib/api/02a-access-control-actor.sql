/*
<pgmi-meta
    id="b264de10-2a62-41b1-a2a7-8e881b255a88"
    idempotent="false">
  <description>
    Declared access, structure, part 2: the refusal log records the actor kind
    of each refused call (api.current_actor_kind()) beside the actual and
    effective principals. Rows logged before carry none.
  </description>
  <sortKeys>
    <key>004/002/001/000</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE api.access_denial
    ADD COLUMN actor_kind text CONSTRAINT access_denial_actor_kind_check CHECK (actor_kind IN ('human', 'agent', 'service', 'system')),
    ADD CONSTRAINT access_denial_actor_kind_not_null NOT NULL actor_kind NOT VALID;

COMMENT ON COLUMN api.access_denial.actor_kind IS
    'What really called: human, agent, service, or system when the call named no principal. NULL only on rows logged before actor kinds were recorded.';
