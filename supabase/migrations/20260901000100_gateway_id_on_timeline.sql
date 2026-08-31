-- Puts the gateway order id on the run timeline, so a money row in the trail can
-- be checked against the payment gateway rather than only against our own tables.
-- The id is lifted from the trail payload when the row carries it, and otherwise
-- read from the order the row points at.

create or replace view public.run_timeline
with (security_invoker = true) as
select
  a.run_id,
  a.created_at as at,
  a.actor,
  a.action,
  a.reason,
  a.order_id,
  case
    when jsonb_typeof(a.payload -> 'amount_paise') = 'number'
      then (a.payload ->> 'amount_paise')::bigint
    when jsonb_typeof(a.payload -> 'run' -> 'final_amount_paise') = 'number'
      then (a.payload -> 'run' ->> 'final_amount_paise')::bigint
  end as amount_paise,
  a.payload,
  coalesce(a.payload ->> 'razorpay_order_id', o.razorpay_order_id) as gateway_order_id
from public.audit_log a
left join public.orders o on o.id = a.order_id
where a.run_id is not null;

grant select on public.run_timeline to authenticated;
