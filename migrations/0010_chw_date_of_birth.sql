-- +goose Up
-- 0010: an optional date of birth on the register, for interoperability.
--
-- eCHIS marks date_of_birth as required on a CHW contact, but CHWR captures age
-- as a snapshot (age_years + age_captured_on), not a birth date. This nullable
-- column lets data entry record a real date of birth over time; until one is
-- present, the read API derives an APPROXIMATE birth date (1 January of the
-- estimated birth year, age_captured_on's year minus age_years) and flags it as
-- approximate, so no consumer mistakes it for an exact date. See the API
-- projection in internal/store/api.go and docs/api.md.
--
-- Nothing derives or writes this column automatically: it is captured, not
-- computed, precisely so a real date is distinguishable from the approximation.
ALTER TABLE chws ADD COLUMN date_of_birth date
    CHECK (date_of_birth IS NULL OR date_of_birth > '1900-01-01');
