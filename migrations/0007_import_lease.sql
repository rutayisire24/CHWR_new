-- +goose Up
-- 0007: one commit at a time, per batch.
--
-- Committing a batch is a loop of one transaction per row, and at the 10,000
-- row cap it runs for something over half a minute (docs/import.md carries the
-- measurement). A page that does nothing for that long invites a second click,
-- and two concurrent runs both read the same page of `ready` rows before either
-- marks them: measured, a 1,200-row file double-submitted created 2,033 CHWs.
--
-- The claim below is what makes that impossible. It is a lease rather than a
-- flag so that a process killed mid-commit does not wedge the batch forever —
-- after the interval another attempt may take it, and the rows already marked
-- `imported` are no longer in the set a commit walks, so resuming is safe.

ALTER TABLE import_batches ADD COLUMN committing_at timestamptz;

COMMENT ON COLUMN import_batches.committing_at IS
    'Held while a commit is running. NULL when idle; a lease, so a crashed run expires.';
