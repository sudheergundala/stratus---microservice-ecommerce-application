-- LOCAL ONLY. One-time setup of the catalog database and its two roles.
-- In AWS the same objects are created by Terraform (postgresql provider) with
-- passwords from Secrets Manager; this file is never used there.
--
-- Run as the Postgres admin user:
--   docker exec -i postgres psql -v ON_ERROR_STOP=1 -U stratus -d postgres < local/catalog-db-bootstrap.sql

-- Owns the schema: used only by the migration Job.
CREATE ROLE catalog_migrator LOGIN PASSWORD 'local-dev-only-migrator';
-- Used by the running service: reads and writes rows, nothing else.
CREATE ROLE catalog_app LOGIN PASSWORD 'local-dev-only-app';

CREATE DATABASE catalog OWNER catalog_migrator;
-- Other services' roles can't even connect to this database.
REVOKE CONNECT ON DATABASE catalog FROM PUBLIC;
GRANT CONNECT ON DATABASE catalog TO catalog_migrator, catalog_app;

\connect catalog
GRANT USAGE ON SCHEMA public TO catalog_app;
-- Every table the migrator creates later is automatically readable and
-- writable by the app. No DELETE: products are retired with active = false.
ALTER DEFAULT PRIVILEGES FOR ROLE catalog_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE ON TABLES TO catalog_app;