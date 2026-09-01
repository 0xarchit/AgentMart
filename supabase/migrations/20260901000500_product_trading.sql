-- What the shop can observe about how each product is actually selling. This is
-- the evidence an opening quote is argued from, so a premium can be explained by
-- something that happened rather than by a constant.
--
-- Cover is expressed in days at the rate observed over the window: a shelf
-- holding one unit while nine moved in a month is inside a week of cover, and a
-- shelf that sold nothing has no measurable cover at all rather than infinite
-- cover. Minus one says unknown, which is deliberately distinguishable from zero.

create or replace view public.product_trading
with (security_invoker = true) as
select
  p.id as product_id,
  p.stock,
  coalesce(sold.units, 0)::bigint as units_sold,
  case
    when coalesce(sold.units, 0) = 0 then -1
    else ((p.stock::bigint * 30) / sold.units)::bigint
  end as stock_cover_days
from public.products p
left join (
  select
    o.product_id,
    sum(o.qty)::bigint as units
  from public.orders o
  where o.status = 'fulfilled_via_wallet'
    and o.created_at >= now() - interval '30 days'
  group by o.product_id
) sold on sold.product_id = p.id;

comment on view public.product_trading is
  'Per product selling rate over the last thirty days, with stock cover in days. Minus one cover means no sales were observed.';
