-- Adds a nullable product image URL. No data: the storefront draws its own
-- placeholder when this is empty, so the column is here to be filled later
-- without another schema change rather than to be filled now.
alter table public.products
  add column if not exists image_url text;

comment on column public.products.image_url is
  'Optional product photograph. Null means the storefront draws a generated placeholder instead.';
