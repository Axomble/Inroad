-- Conditional branching for campaign sequences.
--
-- A branch is the ROUTER attached to one step: it decides where an enrollment
-- goes after that step has been sent. A step with no branch row keeps today's
-- behaviour exactly — it falls through to the next step by step_order — so every
-- existing linear campaign is a campaign with zero rows here and nothing changes
-- for it.
--
-- One row per source step (step_id is the primary key), rather than a generic
-- edge table or a "condition" step kind, for three reasons:
--   * a condition is always evaluated against the step it hangs off ("opened
--     THIS step within N days"), so tying it to that step makes the reference
--     point unambiguous and impossible to dangle;
--   * a non-email node would have to be taught to every send-path query that
--     reads sequence_steps (content, variants, threading, results); a router row
--     is invisible to all of them;
--   * each router has at most two exits (yes/no), so an edge table would only
--     add a way to express three.
--
-- condition = 'always' is the unconditional router: yes_step_id is the next step
-- (NULL = the path ends here). It is what lets a branch END a path, or jump to a
-- step other than the next by order, without inventing a special node.
--
-- Same-campaign targets are enforced by the database, not only by the service:
-- every step reference is a composite FK on (id, campaign_id), so a branch cannot
-- point at another campaign's (or another tenant's) step even if a caller skips
-- validation. Cycles cannot be expressed as a constraint and are rejected by the
-- service at save time (internal/platform/seqgraph), with a runtime backstop in
-- the send path.
--
-- The reply-condition index on inbox_threads lives in its own migration
-- (20260923144758_inbox_threads_campaign_contact_index) so it can be built
-- CONCURRENTLY, without locking a live table inside this file's transaction.

-- Referenceable by the composite FKs below. Redundant with the primary key for
-- uniqueness; exists solely to be referenced, the same shape as
-- campaigns_id_workspace_key (migration 000028).
ALTER TABLE sequence_steps ADD CONSTRAINT sequence_steps_id_campaign_key UNIQUE (id, campaign_id);

CREATE TABLE sequence_step_branches (
    step_id         UUID NOT NULL PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id     UUID NOT NULL,
    condition       TEXT NOT NULL
        CHECK (condition IN ('always', 'opened', 'clicked', 'replied', 'not_opened', 'not_replied')),
    -- The evaluation window, in days after the source step's send. Required for
    -- every real condition, forbidden on 'always' (see the CHECK below).
    within_days     INT CHECK (within_days BETWEEN 1 AND 90),
    -- Optional narrowing of a reply condition to one reply label, by its stable
    -- key (the value inbox_messages.reply_class and sequence_enrollments.
    -- reply_class already store). A key rather than an FK to reply_labels for the
    -- reason migration 000047 gives for reply_class: a label may be deleted, and
    -- the branch must degrade to "never matches" rather than vanish.
    reply_label_key TEXT CHECK (reply_label_key ~ '^[a-z][a-z0-9_]{0,63}$'),
    -- NULL on either exit means that exit ends the path (the enrollment
    -- completes). Deleting a target step therefore turns its incoming exits into
    -- ends rather than blocking the delete or orphaning the reference.
    yes_step_id     UUID,
    no_step_id      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT sequence_step_branches_step_fkey
        FOREIGN KEY (step_id, campaign_id) REFERENCES sequence_steps (id, campaign_id) ON DELETE CASCADE,
    CONSTRAINT sequence_step_branches_campaign_workspace_fkey
        FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns (id, workspace_id) ON DELETE CASCADE,
    -- SET NULL (col) nulls only the target column: a plain SET NULL would also
    -- null campaign_id, which is NOT NULL and shared with the other references.
    -- Column-list SET NULL is PostgreSQL 15+; every supported deployment runs 16.
    CONSTRAINT sequence_step_branches_yes_fkey
        FOREIGN KEY (yes_step_id, campaign_id) REFERENCES sequence_steps (id, campaign_id)
        ON DELETE SET NULL (yes_step_id),
    CONSTRAINT sequence_step_branches_no_fkey
        FOREIGN KEY (no_step_id, campaign_id) REFERENCES sequence_steps (id, campaign_id)
        ON DELETE SET NULL (no_step_id),

    -- A real condition has a window; 'always' has none.
    CONSTRAINT sequence_step_branches_window_chk
        CHECK ((condition = 'always') = (within_days IS NULL)),
    -- 'always' has exactly one exit.
    CONSTRAINT sequence_step_branches_always_single_exit_chk
        CHECK (condition <> 'always' OR no_step_id IS NULL),
    -- A label only narrows a REPLY condition.
    CONSTRAINT sequence_step_branches_label_chk
        CHECK (reply_label_key IS NULL OR condition IN ('replied', 'not_replied')),
    -- A self-reference is the smallest cycle; refused here as well as in the service.
    CONSTRAINT sequence_step_branches_no_self_chk
        CHECK (yes_step_id IS DISTINCT FROM step_id AND no_step_id IS DISTINCT FROM step_id)
);

-- The send path loads a campaign's routers in one read (ListBranchesByCampaign).
CREATE INDEX idx_sequence_step_branches_campaign ON sequence_step_branches (campaign_id, workspace_id);
-- ON DELETE SET NULL of a step looks up the rows referencing it; without these a
-- step delete would scan the table (same reasoning as migration 000030).
CREATE INDEX idx_sequence_step_branches_yes ON sequence_step_branches (yes_step_id) WHERE yes_step_id IS NOT NULL;
CREATE INDEX idx_sequence_step_branches_no ON sequence_step_branches (no_step_id) WHERE no_step_id IS NOT NULL;

-- The current_step at which this enrollment last waited on a branch condition.
--
-- It exists for ONE decision: whether the send path must re-check a step's due
-- time instead of trusting the advance task's schedule. A linear enrollment's
-- advance task is scheduled for exactly the step's due time, so today's path
-- trusts it — but a task scheduled by a condition re-check is not, and if the
-- operator deletes the campaign's last branch while a contact is waiting, that
-- task would otherwise send the linear successor at the re-check time, days
-- ahead of its delay. Stale by construction once current_step moves on, so the
-- cursor advance never has to clear it.
ALTER TABLE sequence_enrollments ADD COLUMN awaiting_condition_step INT;
