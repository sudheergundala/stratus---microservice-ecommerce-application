-- Expand step: new nullable column. Adding a nullable column without a
-- default only changes the catalog metadata, so it is instant on any table size.
-- The app starts using it in a later release (expand, then migrate code, then contract).
ALTER TABLE products ADD COLUMN brand text CHECK (length(brand) <= 100);
