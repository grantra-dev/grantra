-- Identity sources can read an HR system themselves (Workday, BambooHR)
-- instead of being sent files. How to reach it is in the source's config,
-- under connection, with references to its secrets.
ALTER TABLE identity_sources DROP CONSTRAINT identity_sources_kind_check;
ALTER TABLE identity_sources ADD CONSTRAINT identity_sources_kind_check CHECK (kind IN ('csv', 'workday', 'bamboohr'));
