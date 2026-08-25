-- +goose Up
-- 0008: the record a staged row resolved to.
--
-- Until now a commit rebuilt the register record by re-parsing import_rows.raw.
-- That worked while every field was a pure function of its own cell — a name is
-- a name, an age is an age — but the profile columns are not. A facility is
-- resolved by name within the CHW's district, and re-resolving it at commit
-- would answer from a register that has moved since the report: a facility
-- renamed between the two requests would silently change which one a CHW
-- reports to, or fail a row the operator was shown as ready.
--
-- So the resolved record is stored beside the raw one. `raw` stays exactly as
-- the file wrote it and answers "what did the file say"; `record` is what the
-- commit will write, and makes "what is committed is what was reviewed" literal
-- rather than argued.

ALTER TABLE import_rows ADD COLUMN record jsonb;

COMMENT ON COLUMN import_rows.record IS
    'The resolved register record this row will create. NULL for a refused row.';
