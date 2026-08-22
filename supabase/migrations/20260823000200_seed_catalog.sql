-- Deterministic catalog data for local development and the buildathon demo.

insert into public.products (id, name, category, price_paise, stock, warranty_years, trust_score, combo_discount_pct)
values
  ('00000000-0000-0000-0000-000000000001', 'TrimPro Basic', 'trimmer', 200000, 20, 1, 78, 0),
  ('00000000-0000-0000-0000-000000000002', 'TrimPro Shield', 'trimmer', 240000, 20, 3, 92, 0),
  ('00000000-0000-0000-0000-000000000003', 'TrimPro Max', 'trimmer', 285000, 12, 5, 95, 10),
  ('00000000-0000-0000-0000-000000000101', 'CalmSkin Daily Cream', 'cream', 45000, 40, 0, 84, 0),
  ('00000000-0000-0000-0000-000000000102', 'CalmSkin Repair Cream', 'cream', 65000, 30, 0, 87, 0),
  ('00000000-0000-0000-0000-000000000103', 'CalmSkin SPF Cream', 'cream', 75000, 30, 0, 89, 0),
  ('00000000-0000-0000-0000-000000000104', 'CalmSkin Night Cream', 'cream', 85000, 25, 0, 90, 0)
on conflict (id) do update set
  name = excluded.name,
  category = excluded.category,
  price_paise = excluded.price_paise,
  warranty_years = excluded.warranty_years,
  trust_score = excluded.trust_score,
  combo_discount_pct = excluded.combo_discount_pct;

update public.products
set combo_with = '00000000-0000-0000-0000-000000000101'
where id = '00000000-0000-0000-0000-000000000001';

update public.products
set combo_with = '00000000-0000-0000-0000-000000000102'
where id = '00000000-0000-0000-0000-000000000002';

update public.products
set combo_with = '00000000-0000-0000-0000-000000000103'
where id = '00000000-0000-0000-0000-000000000003';
