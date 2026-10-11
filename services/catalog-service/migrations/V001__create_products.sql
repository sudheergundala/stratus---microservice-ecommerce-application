-- Products shown in the storefront. Images are NOT stored here: image_key
-- points at an object in S3, served through CloudFront.
CREATE TABLE products (
    sku          text        PRIMARY KEY CHECK (sku ~ '^[A-Z0-9-]{3,32}$'),
    name         text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description  text        NOT NULL DEFAULT '' CHECK (length(description) <= 5000),
    category     text        NOT NULL CHECK (category ~ '^[a-z0-9-]{2,40}$'),
    price_cents  integer     NOT NULL CHECK (price_cents >= 0),
    currency     char(3)     NOT NULL DEFAULT 'USD',
    image_key    text,
    active       boolean     NOT NULL DEFAULT true,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- Kept up to date by PostgreSQL on every insert/update: name matches rank
    -- above description matches.
    search       tsvector    GENERATED ALWAYS AS (
                     setweight(to_tsvector('english', name), 'A') ||
                     setweight(to_tsvector('english', description), 'B')
                 ) STORED
);

-- Full-text search.
CREATE INDEX products_search_idx ON products USING gin (search);

-- Browse a category sorted by price (keyset pagination on price, then sku).
CREATE INDEX products_category_price_idx ON products (category, price_cents, sku) WHERE active;

-- Browse everything sorted by price.
CREATE INDEX products_price_idx ON products (price_cents, sku) WHERE active;