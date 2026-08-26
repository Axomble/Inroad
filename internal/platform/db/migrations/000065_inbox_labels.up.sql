-- Operator-assignable thread labels: the inbox's own filing system.
--
-- Deliberately NOT reply_labels. That table is an AUTO-CLASSIFICATION
-- taxonomy — the model writes `inbox_threads.last_reply_class`, and a member
-- cannot assign it. These are the opposite: created and applied by hand, with
-- no classifier involvement, and many-to-many rather than one-per-thread.
-- Folding the two together would mean either letting a human overwrite the
-- classifier's verdict or letting the classifier stomp a human's filing.
CREATE TABLE inbox_labels (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    -- A hex colour (#rrggbb), validated at the service boundary rather than by
    -- a CHECK: the set of acceptable formats is a product decision that will
    -- change (named colours, oklch) more often than we want to write
    -- migrations, and a bad value here is cosmetic, not corrupting.
    color        TEXT NOT NULL DEFAULT '#94a3b8',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness per workspace: "Invoices" and "invoices" are the
-- same label to an operator, and a search-or-create picker that produced both
-- would quietly split one pile in two. lower() rather than CITEXT because the
-- comparison is only needed here, and an expression index keeps the column a
-- plain TEXT that preserves the capitalisation the member typed.
CREATE UNIQUE INDEX uq_inbox_labels_workspace_name
    ON inbox_labels (workspace_id, lower(name));

-- The join. Composite PK on (thread_id, label_id) makes "a label applies to a
-- thread at most once" a schema guarantee, so applying an already-applied
-- label is idempotent rather than an error to handle.
CREATE TABLE inbox_thread_labels (
    thread_id    UUID NOT NULL REFERENCES inbox_threads(id) ON DELETE CASCADE,
    label_id     UUID NOT NULL REFERENCES inbox_labels(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (thread_id, label_id)
);

-- Both directions are hot: "which labels does this thread carry" (the reader)
-- and "which threads carry this label" (the rail's label scope). The PK serves
-- the first; this index serves the second.
CREATE INDEX idx_inbox_thread_labels_label
    ON inbox_thread_labels (workspace_id, label_id);
