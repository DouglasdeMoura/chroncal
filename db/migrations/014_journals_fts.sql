-- +goose Up

-- Trigger-maintained FTS5 table for full-text search on journals.

CREATE VIRTUAL TABLE journals_fts USING fts5(
    summary, description, categories,
    tokenize='unicode61 remove_diacritics 2'
);

-- ── Journal triggers ────────────────────────────────────────────────────

-- Categories are inserted separately, so the initial FTS row has empty categories.
-- +goose StatementBegin
CREATE TRIGGER journals_fts_ai AFTER INSERT ON journals BEGIN
    INSERT INTO journals_fts(rowid, summary, description, categories)
    VALUES (NEW.id, NEW.summary, COALESCE(NEW.description, ''), '');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER journals_fts_au AFTER UPDATE ON journals BEGIN
    DELETE FROM journals_fts WHERE rowid = OLD.id;
    INSERT INTO journals_fts(rowid, summary, description, categories)
    VALUES (NEW.id, NEW.summary, COALESCE(NEW.description, ''),
        COALESCE((SELECT GROUP_CONCAT(jc.category, ' ')
                  FROM journal_categories jc WHERE jc.journal_id = NEW.id), ''));
END;
-- +goose StatementEnd

-- BEFORE DELETE so it fires before CASCADE removes categories.
-- +goose StatementBegin
CREATE TRIGGER journals_fts_bd BEFORE DELETE ON journals BEGIN
    DELETE FROM journals_fts WHERE rowid = OLD.id;
END;
-- +goose StatementEnd

-- ── Journal category triggers ───────────────────────────────────────────

-- +goose StatementBegin
CREATE TRIGGER journal_categories_fts_ai AFTER INSERT ON journal_categories BEGIN
    DELETE FROM journals_fts WHERE rowid = NEW.journal_id;
    INSERT INTO journals_fts(rowid, summary, description, categories)
    SELECT j.id, j.summary, COALESCE(j.description, ''),
        COALESCE((SELECT GROUP_CONCAT(jc.category, ' ')
                  FROM journal_categories jc WHERE jc.journal_id = j.id), '')
    FROM journals j WHERE j.id = NEW.journal_id;
END;
-- +goose StatementEnd

-- Guard: skip when the journal itself was CASCADE-deleted (row already gone).
-- +goose StatementBegin
CREATE TRIGGER journal_categories_fts_ad AFTER DELETE ON journal_categories
WHEN EXISTS (SELECT 1 FROM journals WHERE id = OLD.journal_id) BEGIN
    DELETE FROM journals_fts WHERE rowid = OLD.journal_id;
    INSERT INTO journals_fts(rowid, summary, description, categories)
    SELECT j.id, j.summary, COALESCE(j.description, ''),
        COALESCE((SELECT GROUP_CONCAT(jc.category, ' ')
                  FROM journal_categories jc WHERE jc.journal_id = j.id), '')
    FROM journals j WHERE j.id = OLD.journal_id;
END;
-- +goose StatementEnd

-- ── Backfill ────────────────────────────────────────────────────────────

INSERT INTO journals_fts (rowid, summary, description, categories)
SELECT j.id, j.summary, COALESCE(j.description, ''),
       COALESCE((SELECT GROUP_CONCAT(jc.category, ' ')
                 FROM journal_categories jc WHERE jc.journal_id = j.id), '')
FROM journals j;

-- +goose Down

DROP TRIGGER IF EXISTS journal_categories_fts_ad;
DROP TRIGGER IF EXISTS journal_categories_fts_ai;
DROP TRIGGER IF EXISTS journals_fts_bd;
DROP TRIGGER IF EXISTS journals_fts_au;
DROP TRIGGER IF EXISTS journals_fts_ai;

DROP TABLE IF EXISTS journals_fts;
