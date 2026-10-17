-- Development-only bootstrap. Grantra connects as a non-superuser role so that
-- row-level security applies to it (superusers bypass RLS).
CREATE ROLE grantra_app LOGIN PASSWORD 'grantra' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE grantra OWNER grantra_app;

CREATE ROLE keycloak LOGIN PASSWORD 'keycloak';
CREATE DATABASE keycloak OWNER keycloak;

-- The demo's shop database, which people reach through the Grantra gateway
-- (psql on localhost:15432 with a token). The gateway logs in as its owner
-- with the password kept in Grantra's vault.
CREATE ROLE shop_owner LOGIN PASSWORD 'shop-demo-password';
CREATE DATABASE shop OWNER shop_owner;
\connect shop
SET ROLE shop_owner;
CREATE TABLE orders (
    id       int PRIMARY KEY,
    customer text NOT NULL,
    total    numeric(10, 2) NOT NULL,
    placed   date NOT NULL
);
INSERT INTO orders VALUES
    (1001, 'Marta Ruiz', 59.90, '2026-10-01'),
    (1002, 'Jon Etxeberria', 120.00, '2026-10-03'),
    (1003, 'Lucía Romero', 18.50, '2026-10-07');
