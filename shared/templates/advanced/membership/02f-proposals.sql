/*
<pgmi-meta
    id="27296886-198e-446b-bc6c-be27b2d2bb1c"
    idempotent="false">
  <description>
    Membership structure, part 9: proposals. A proposal tool (an MCP tool
    with effect proposal) records the write a caller, usually an agent,
    wants made: the target write tool and its arguments, in the request's
    organization. Nothing else changes until a person, acting as
    themselves, confirms it, which runs the target as that person, or
    rejects it; either way it is decided once. The organization's members
    read its proposals (policy in 06a-proposal-rls.sql); only the kernels
    write them.
  </description>
  <sortKeys>
    <key>005/000/002/006</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE membership.proposal (
    object_id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    organization_object_id uuid        NOT NULL REFERENCES membership.organization (object_id),
    tool_name              text        NOT NULL
        CONSTRAINT proposal_tool_name_check CHECK (membership.fn_is_clean_name(tool_name, 255)),
    arguments              jsonb       NOT NULL
        CONSTRAINT proposal_arguments_check CHECK (jsonb_typeof(arguments) = 'object'),
    proposed_by_object_id  uuid        NOT NULL REFERENCES membership.principal (object_id),
    proposer_kind          text        NOT NULL
        CONSTRAINT proposal_proposer_kind_check CHECK (proposer_kind IN ('human', 'agent', 'service')),
    status                 text        NOT NULL DEFAULT 'pending'
        CONSTRAINT proposal_status_check CHECK (status IN ('pending', 'confirmed', 'rejected')),
    proposed_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    decided_by_object_id   uuid        REFERENCES membership.principal (object_id),
    decided_at             timestamptz,
    CONSTRAINT proposal_decided_check
        CHECK ((status = 'pending') = (decided_at IS NULL) AND (decided_at IS NULL) = (decided_by_object_id IS NULL))
);

CREATE INDEX ix_proposal_organization ON membership.proposal (organization_object_id, status, proposed_at);

COMMENT ON TABLE membership.proposal IS
    'A write a caller proposed through a proposal tool: the target write tool and its arguments, in one organization. Pending until a person acting as themselves, holding proposals.decide, confirms it (the target runs as that person) or rejects it; decided once.';
COMMENT ON COLUMN membership.proposal.proposed_by_object_id IS
    'The actual principal that proposed it: the agent, when an agent called the proposal tool.';
COMMENT ON COLUMN membership.proposal.arguments IS
    'The target tool''s arguments as the proposer sent them: untrusted stored text, data and never instruction.';

ALTER TABLE membership.proposal ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.proposal FORCE ROW LEVEL SECURITY;
SELECT pg_temp.kernel_passes('membership.proposal', '{select,insert,update}');

SELECT pg_temp.exempt_from_fk_index('membership.proposal', '{proposed_by_object_id}', 'records who acted; principals are never deleted');
SELECT pg_temp.exempt_from_fk_index('membership.proposal', '{decided_by_object_id}', 'records who acted; principals are never deleted');
