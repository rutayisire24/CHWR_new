-- +goose Up
-- 0007: cadres an administrator can add, and cannot corrupt
--
-- 0003 made cadres data so that a new one is an INSERT. This makes that INSERT
-- something a national administrator can do from the UI, which means the table
-- now takes writes from people rather than from migrations, and the rules a
-- migration author kept in their head have to live in the schema instead.

------------------------------------------------------------ vocabulary shape
-- Slugs travel in URLs (?cadre=vht), in staged import records read back weeks
-- later, and in the export's cadre column. Lower-case, starting with a letter,
-- no spaces: what survives all three unescaped.
ALTER TABLE cadre_categories
    ADD CONSTRAINT cadre_categories_slug_shape CHECK (slug ~ '^[a-z][a-z0-9_]{1,31}$'),
    ADD CONSTRAINT cadre_categories_label_present CHECK (btrim(label) <> '');

ALTER TABLE cadres
    ADD CONSTRAINT cadres_slug_shape CHECK (slug ~ '^[a-z][a-z0-9_]{1,31}$'),
    ADD CONSTRAINT cadres_label_present CHECK (btrim(label) <> ''),
    -- The placement cascade reaches district > subcounty > parish > village.
    -- Region has no district ancestor, so the placement trigger would refuse
    -- every deployment; county is never selected in the UI. A cadre at either
    -- level would be one nobody could deploy.
    ADD CONSTRAINT cadres_placement_selectable
        CHECK (placement_level IN ('district','subcounty','parish','village'));

-- The slug is unique within a category (0003), but the filter, the export and
-- the importer all name a cadre by slug alone. Two categories sharing one would
-- make ?cadre=nurse mean two things.
CREATE UNIQUE INDEX cadres_slug_uniq ON cadres (slug);

--------------------------------------------------- frozen once anyone serves
-- The placement trigger checks a deployment when the deployment changes, not
-- when its cadre does. Moving VHTs from village to parish after 40,000 of them
-- are deployed would leave every one of those rows violating invariant 1 with
-- nothing to notice. Likewise a category change would move serving workers onto
-- another category's profile surface, and a slug change would orphan staged
-- import rows that name the old one.
--
-- So a cadre is editable freely until its first deployment, and afterwards only
-- in what it is called (label), how imports spell it (aliases), where it sorts,
-- and whether it is still offered (active). A cadre that has to change shape is
-- retired and a new one added; workers move to it by an ordinary re-cadring,
-- which ends and opens postings and so passes through the placement trigger.
-- +goose StatementBegin
CREATE FUNCTION cadres_freeze_when_used() RETURNS trigger AS $$
BEGIN
    IF (NEW.slug, NEW.category_id, NEW.placement_level)
       IS DISTINCT FROM (OLD.slug, OLD.category_id, OLD.placement_level)
       AND EXISTS (SELECT 1 FROM deployments WHERE cadre_id = OLD.id) THEN
        RAISE EXCEPTION 'cadre % has deployments; its slug, category and placement level are fixed — retire it and add a new cadre instead',
            OLD.slug;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER cadres_freeze_trg
    BEFORE UPDATE OF slug, category_id, placement_level ON cadres
    FOR EACH ROW EXECUTE FUNCTION cadres_freeze_when_used();
