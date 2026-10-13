-- Development-only bootstrap. Grantra connects as a non-superuser role so that
-- row-level security applies to it (superusers bypass RLS).
CREATE ROLE grantra_app LOGIN PASSWORD 'grantra' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE grantra OWNER grantra_app;

CREATE ROLE keycloak LOGIN PASSWORD 'keycloak';
CREATE DATABASE keycloak OWNER keycloak;
