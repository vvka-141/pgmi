/*
<pgmi-meta
    id="51703460-3273-452b-9514-59898e849f00"
    idempotent="true">
  <description>
    Proposal kernels: an agent proposes a write, a person confirms it. A
    proposal tool's body calls membership.fn_propose, which records the
    target write tool and its arguments in the request's organization and
    returns at once; the request never waits for a person. Only a person
    acting as themselves decides (the same rule as approving a connection),
    and only a pending proposal: membership.fn_decide_proposal locks it,
    and on confirm runs the target through the MCP dispatcher as the
    confirming person, so the target's own permission check applies to the
    confirmer, as if they had called it. A target that answers an error
    leaves the proposal pending and hands back that answer. The kernel role
    owns the SECURITY DEFINER routines; the decision itself runs as the
    caller, because the target handler must run as every handler does.
  </description>
  <sortKeys>
    <key>005/000/005/003</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT core.ensure_view('membership.vw_proposal', $view$
CREATE OR REPLACE VIEW membership.vw_proposal
WITH (security_invoker = true) AS
SELECT p.object_id AS proposal_object_id,
       p.organization_object_id,
       p.tool_name,
       p.arguments,
       p.proposed_by_object_id,
       p.proposer_kind,
       p.status,
       p.proposed_at,
       p.decided_by_object_id,
       p.decided_at
FROM membership.proposal p
$view$);

COMMENT ON VIEW membership.vw_proposal IS
    'Proposals of the reader''s organization: pending, confirmed or rejected. arguments is the proposer''s stored text.';

-- One proposal as the API answers it, the same in the list and after a decision.
CREATE OR REPLACE FUNCTION membership.fn_proposal_json(p membership.vw_proposal)
RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_strip_nulls(jsonb_build_object(
        'proposalId', p.proposal_object_id,
        'toolName', p.tool_name,
        'proposedBy', p.proposed_by_object_id,
        'proposerKind', p.proposer_kind,
        'status', p.status,
        'proposedAt', p.proposed_at,
        'decidedBy', p.decided_by_object_id,
        'decidedAt', p.decided_at))
        || jsonb_build_object('arguments', p.arguments);
$$;

-- Records a pending proposal of p_tool with p_arguments by the request's
-- actual principal in its organization; NULL without an organization, a
-- principal or an arguments object, or when p_tool is not a live write tool.
CREATE OR REPLACE FUNCTION membership.fn_propose(p_tool text, p_arguments jsonb)
RETURNS uuid
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    INSERT INTO membership.proposal (organization_object_id, tool_name, arguments, proposed_by_object_id, proposer_kind)
    SELECT r.organization, p_tool, p_arguments, r.actor, api.current_actor_kind()
    FROM (SELECT api.current_organization_id() AS organization, api.current_actual_principal_id() AS actor) r
    WHERE r.organization IS NOT NULL AND r.actor IS NOT NULL AND jsonb_typeof(p_arguments) = 'object'
      AND internal.mcp_write_tool_exists(p_tool)
    RETURNING object_id;
$$;

SELECT pg_temp.kernel_owns('membership.fn_propose(text, jsonb)');

-- Why the request cannot decide this proposal, or NULL:
-- membership.proposal.not_found (none in the request's organization) |
-- membership.proposal.human_required (only a person acting as themselves
-- decides; an agent never does) | membership.proposal.decided.
CREATE OR REPLACE FUNCTION membership.fn_proposal_problem(p_proposal_object_id uuid)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN p.object_id IS NULL THEN 'membership.proposal.not_found'
        WHEN api.current_actor_kind() IS DISTINCT FROM 'human' THEN 'membership.proposal.human_required'
        WHEN p.status <> 'pending' THEN 'membership.proposal.decided'
    END
    FROM (SELECT) one
    LEFT JOIN membership.proposal p
           ON p.object_id = p_proposal_object_id AND p.organization_object_id = api.current_organization_id();
$$;

SELECT pg_temp.kernel_owns('membership.fn_proposal_problem(uuid)');

-- The pending proposal, locked until the request ends, when the request may
-- decide it; no row otherwise. A concurrent decision waits here and then
-- finds it decided.
CREATE OR REPLACE FUNCTION membership.fn_claim_proposal(p_proposal_object_id uuid)
RETURNS membership.proposal
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT p.*
    FROM membership.proposal p
    WHERE p.object_id = p_proposal_object_id
      AND p.organization_object_id = api.current_organization_id()
      AND p.status = 'pending'
      AND membership.fn_proposal_problem(p.object_id) IS NULL
    FOR UPDATE;
$$;

SELECT pg_temp.kernel_owns('membership.fn_claim_proposal(uuid)');

CREATE OR REPLACE FUNCTION membership.fn_settle_proposal(p_proposal_object_id uuid, p_status text)
RETURNS membership.proposal
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    UPDATE membership.proposal p
    SET status = p_status, decided_by_object_id = api.current_actual_principal_id(), decided_at = clock_timestamp()
    WHERE p.object_id = p_proposal_object_id
      AND p.organization_object_id = api.current_organization_id()
      AND p.status = 'pending'
      AND p_status IN ('confirmed', 'rejected')
      AND membership.fn_proposal_problem(p.object_id) IS NULL
    RETURNING p.*;
$$;

SELECT pg_temp.kernel_owns('membership.fn_settle_proposal(uuid, text)');

-- Confirms (p_confirm true) or rejects a pending proposal as the request's
-- person. proposal is the row after the decision; NULL when the request may
-- not decide it (membership.fn_proposal_problem says why). On confirm,
-- answer is the target tool's MCP envelope; when it is an error, nothing
-- changed and proposal is still pending.
CREATE OR REPLACE FUNCTION membership.fn_decide_proposal(
    p_proposal_object_id uuid,
    p_confirm   boolean,
    OUT proposal membership.proposal,
    OUT answer   jsonb)
LANGUAGE plpgsql
SET search_path = membership, api, internal, extensions, common, pg_temp
AS $$
DECLARE
    v_claimed membership.proposal := membership.fn_claim_proposal(p_proposal_object_id);
BEGIN
    IF p_confirm IS NULL OR v_claimed.object_id IS NULL THEN
        RETURN;
    END IF;
    IF p_confirm THEN
        answer := (internal.mcp_dispatch('tool', v_claimed.tool_name, v_claimed.arguments, NULL,
                                         to_jsonb(v_claimed.object_id::text), false)).envelope;
        IF answer ? 'error' OR answer->'result'->'isError' = 'true'::jsonb THEN
            proposal := v_claimed;
            RETURN;
        END IF;
    END IF;
    proposal := membership.fn_settle_proposal(v_claimed.object_id, CASE WHEN p_confirm THEN 'confirmed' ELSE 'rejected' END);
END;
$$;
