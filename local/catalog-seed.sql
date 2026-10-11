-- LOCAL ONLY. Sample products for trying the API. Seed data is not a
-- migration: real product data comes from the merchandising team through the API.
--
--   docker exec -i postgres psql -v ON_ERROR_STOP=1 -U catalog_app -d catalog < local/catalog-seed.sql
INSERT INTO products (sku, name, description, category, price_cents, image_key) VALUES
  ('JKT-001', 'Alpine Shell Jacket',   'Waterproof three-layer shell for wet mountain days.',  'jackets', 18900, 'products/JKT-001/main.webp'),
  ('JKT-002', 'Insulated Parka',       'Warm winter parka with a down-free synthetic fill.',   'jackets', 22900, 'products/JKT-002/main.webp'),
  ('JKT-003', 'Packable Wind Jacket',  'Light windproof layer that packs into its own pocket.', 'jackets',  7900, 'products/JKT-003/main.webp'),
  ('BTS-001', 'Trail Running Shoes',   'Grippy outsole for muddy trails.',                     'shoes',   12900, 'products/BTS-001/main.webp'),
  ('BTS-002', 'Waterproof Hiking Boots','Leather boots with a waterproof membrane.',           'shoes',   17900, 'products/BTS-002/main.webp'),
  ('HAT-001', 'Merino Beanie',         'Soft wool beanie; pairs with any winter jacket.',      'hats',     2900, 'products/HAT-001/main.webp'),
  ('BAG-001', 'Daypack 22L',           'Light hiking backpack with a rain cover.',             'bags',     8900, 'products/BAG-001/main.webp'),
  ('BAG-002', 'Waterproof Dry Bag',    'Roll-top bag that keeps gear dry on the water.',       'bags',     3900, NULL)
ON CONFLICT (sku) DO NOTHING;