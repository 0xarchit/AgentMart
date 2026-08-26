-- Richer demo catalog: 18 products across 8 categories with real-looking
-- pricing, stock spread, warranties, trust scores, merchant cost floors, and
-- cross-category combo pairs so upsell/cross-sell and negotiation have texture.
--
-- Safe to re-run: every row upserts by id.
--
-- Stock is deliberately uneven so the merchant graph exercises all three of its
-- campaign signals: >= 25 units earns a clearance budget, <= 3 units is scarce,
-- everything else is standard.

insert into public.products
  (id, name, category, price_paise, stock, warranty_years, trust_score, combo_discount_pct, cost_paise)
values
  -- Trimmers -------------------------------------------------------------
  ('00000000-0000-0000-0000-000000000201', 'TrimPro Nova 5-in-1',        'trimmer',      179900, 32, 2, 86, 15, 121000),
  ('00000000-0000-0000-0000-000000000202', 'TrimPro Titan XL',           'trimmer',      269900,  8, 4, 94, 20, 182000),
  ('00000000-0000-0000-0000-000000000203', 'TrimPro Lite Everyday',      'trimmer',      129900, 45, 1, 74, 10,  88000),
  ('00000000-0000-0000-0000-000000000204', 'BladeMaster Pro 9',          'trimmer',      349900,  3, 5, 96, 25, 240000),

  -- Shavers --------------------------------------------------------------
  ('00000000-0000-0000-0000-000000000211', 'GlideShave Aqua Wet/Dry',    'shaver',       219900, 18, 2, 88, 15, 150000),
  ('00000000-0000-0000-0000-000000000212', 'GlideShave Rotary 3D',       'shaver',       299900,  6, 3, 92, 20, 205000),

  -- Creams ---------------------------------------------------------------
  ('00000000-0000-0000-0000-000000000301', 'CalmSkin Aloe Soothing Gel', 'cream',         34900, 60, 0, 82,  0,  21000),
  ('00000000-0000-0000-0000-000000000302', 'CalmSkin Vitamin C Cream',   'cream',         49900, 38, 0, 85,  0,  31000),
  ('00000000-0000-0000-0000-000000000303', 'CalmSkin Retinol Night',     'cream',         89900, 22, 0, 90,  0,  56000),
  ('00000000-0000-0000-0000-000000000304', 'CalmSkin SPF 50 Daily',      'cream',         59900, 41, 0, 88,  0,  37000),

  -- Beard oils -----------------------------------------------------------
  ('00000000-0000-0000-0000-000000000401', 'RootRitual Beard Oil',       'beard_oil',     39900, 52, 0, 84,  0,  24000),
  ('00000000-0000-0000-0000-000000000402', 'RootRitual Argan Elixir',    'beard_oil',     69900, 26, 0, 89,  0,  43000),

  -- Face wash ------------------------------------------------------------
  ('00000000-0000-0000-0000-000000000501', 'PureDaily Charcoal Wash',    'face_wash',     29900, 70, 0, 80,  0,  18000),
  ('00000000-0000-0000-0000-000000000502', 'PureDaily Neem Wash',        'face_wash',     24900, 65, 0, 78,  0,  15000),

  -- Hair dryers ----------------------------------------------------------
  ('00000000-0000-0000-0000-000000000601', 'AeroDry 2200 Ionic',         'hair_dryer',    249900, 12, 2, 87, 15, 172000),
  ('00000000-0000-0000-0000-000000000602', 'AeroDry Compact Travel',     'hair_dryer',    149900, 24, 1, 81,  0, 101000),

  -- Serum ----------------------------------------------------------------
  ('00000000-0000-0000-0000-000000000701', 'SilkStrand Heat Serum',      'serum',          44900, 48, 0, 86,  0,  27000),

  -- Straightener ---------------------------------------------------------
  ('00000000-0000-0000-0000-000000000801', 'SilkStrand Ceramic Pro',     'straightener',  279900,  9, 3, 91, 20, 191000)
on conflict (id) do update set
  name               = excluded.name,
  category           = excluded.category,
  price_paise        = excluded.price_paise,
  stock              = excluded.stock,
  warranty_years     = excluded.warranty_years,
  trust_score        = excluded.trust_score,
  combo_discount_pct = excluded.combo_discount_pct,
  cost_paise         = excluded.cost_paise;

-- Cross-sell pairs, set after insert because combo_with references products(id).
-- Each pair crosses categories so the merchant's bundle pitch is a genuine
-- add-on rather than a second unit of the same thing.
update public.products set combo_with = '00000000-0000-0000-0000-000000000401' where id = '00000000-0000-0000-0000-000000000201'; -- Nova      + beard oil
update public.products set combo_with = '00000000-0000-0000-0000-000000000402' where id = '00000000-0000-0000-0000-000000000202'; -- Titan XL  + argan elixir
update public.products set combo_with = '00000000-0000-0000-0000-000000000501' where id = '00000000-0000-0000-0000-000000000203'; -- Lite      + charcoal wash
update public.products set combo_with = '00000000-0000-0000-0000-000000000303' where id = '00000000-0000-0000-0000-000000000204'; -- Pro 9     + retinol night
update public.products set combo_with = '00000000-0000-0000-0000-000000000502' where id = '00000000-0000-0000-0000-000000000211'; -- Aqua      + neem wash
update public.products set combo_with = '00000000-0000-0000-0000-000000000304' where id = '00000000-0000-0000-0000-000000000212'; -- Rotary 3D + SPF 50
update public.products set combo_with = '00000000-0000-0000-0000-000000000701' where id = '00000000-0000-0000-0000-000000000601'; -- AeroDry   + heat serum
update public.products set combo_with = '00000000-0000-0000-0000-000000000701' where id = '00000000-0000-0000-0000-000000000801'; -- Ceramic   + heat serum

-- Keep the original seven demo rows consistent with the cost-floor model in
-- case only the first seed ran.
update public.products set cost_paise = 140000 where id = '00000000-0000-0000-0000-000000000001' and cost_paise = 0;
update public.products set cost_paise = 170000 where id = '00000000-0000-0000-0000-000000000002' and cost_paise = 0;
update public.products set cost_paise = 200000 where id = '00000000-0000-0000-0000-000000000003' and cost_paise = 0;
update public.products set cost_paise =  30000 where id = '00000000-0000-0000-0000-000000000101' and cost_paise = 0;
update public.products set cost_paise =  42000 where id = '00000000-0000-0000-0000-000000000102' and cost_paise = 0;
update public.products set cost_paise =  50000 where id = '00000000-0000-0000-0000-000000000103' and cost_paise = 0;
update public.products set cost_paise =  56000 where id = '00000000-0000-0000-0000-000000000104' and cost_paise = 0;

-- Sanity: nothing may be priced at or below its own cost, or the merchant
-- agent would have no margin to negotiate inside.
do $$
declare bad integer;
begin
  select count(*) into bad from public.products where cost_paise >= price_paise;
  if bad > 0 then
    raise exception 'seed produced % product(s) priced at or below cost', bad;
  end if;
end $$;
