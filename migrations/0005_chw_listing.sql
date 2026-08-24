-- +goose Up
-- 0005: indexes for the register listing.
--
-- 0003 indexed the register for the questions the schema itself asks — scope
-- (district_id, status), placement, cadre, duplicate probing — plus a trigram
-- index for name search. Browsing is a different access pattern: a stable sort
-- by name, paged through with a keyset, inside a scope.
--
-- Sorting by name needs an index on the sort expression or every page is a sort
-- of the whole scope. lower() because the source data is inconsistently cased:
-- the register holds "Okello", "OKELLO" and "okello" for the same surname
-- convention, and a case-sensitive sort interleaves them.

-- The national listing, and the keyset comparison that pages through it.
CREATE INDEX chws_name_sort_idx ON chws (lower(last_name), lower(first_name), id);

-- The district listing. A district user's every page is filtered to their
-- district first, so the district column leads: without it, PostgreSQL scans
-- the national name order and discards 145 districts' worth of rows per page.
CREATE INDEX chws_district_name_idx ON chws (district_id, lower(last_name), lower(first_name), id);

-- NIN lookup by prefix. chws_nin_uniq answers equality, but a clerk holding a
-- paper form types the first characters and expects the match to narrow;
-- text_pattern_ops is what makes LIKE 'CM90%' an index scan.
CREATE INDEX chws_nin_prefix_idx ON chws (nin text_pattern_ops) WHERE nin IS NOT NULL;

