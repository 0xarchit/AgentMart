-- Campaign layer: merchant-funded, per-account deals that grow revenue without
-- selling below cost. Eligibility is computed from real order history so the
-- merchant agent personalises offers from facts, not guesses.

create table if not exists public.campaigns (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  tier text not null,
  discount_pct integer not null check (discount_pct between 0 and 40),
  min_orders integer not null default 0 check (min_orders >= 0),
  min_spend_paise bigint not null default 0 check (min_spend_paise >= 0),
  priority integer not null default 0,
  active boolean not null default true,
  starts_at timestamptz not null default now(),
  ends_at timestamptz,
  created_at timestamptz not null default now()
);

create index if not exists campaigns_active_priority_idx
  on public.campaigns(active, priority desc);

alter table public.campaigns enable row level security;

-- Campaign definitions are merchant marketing copy: readable by everyone so the
-- storefront can advertise them. Eligibility (which needs order history) stays
-- behind the trusted RPC below.
drop policy if exists campaigns_select_public on public.campaigns;
create policy campaigns_select_public
  on public.campaigns for select to anon, authenticated using (active);

insert into public.campaigns (name, tier, discount_pct, min_orders, min_spend_paise, priority)
values
  ('Welcome offer',      'welcome',  5,  0, 0,       10),
  ('Returning customer', 'returning', 8, 2, 300000,  20),
  ('Loyal customer',     'loyal',    12, 5, 1000000, 30)
on conflict do nothing;

-- campaign_for_account returns the best campaign a buyer currently qualifies
-- for, with the history that justified it. Read-only: it never moves money.
create or replace function public.campaign_for_account(p_account_id uuid)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare
  order_count integer := 0;
  spend_paise bigint := 0;
  chosen public.campaigns%rowtype;
begin
  select count(*), coalesce(sum(amount_paise), 0)
    into order_count, spend_paise
  from public.orders
  where account_id = p_account_id
    and status in ('fulfilled_via_wallet', 'refunded_via_wallet');

  select * into chosen
  from public.campaigns
  where active
    and starts_at <= now()
    and (ends_at is null or ends_at > now())
    and min_orders <= order_count
    and min_spend_paise <= spend_paise
  order by priority desc, discount_pct desc
  limit 1;

  if not found then
    return jsonb_build_object(
      'tier', 'standard',
      'discount_pct', 0,
      'orders', order_count,
      'spend_paise', spend_paise,
      'notes', jsonb_build_array('no campaign matched this buyer''s history')
    );
  end if;

  return jsonb_build_object(
    'tier', chosen.tier,
    'discount_pct', chosen.discount_pct,
    'campaign', chosen.name,
    'orders', order_count,
    'spend_paise', spend_paise,
    'notes', jsonb_build_array(
      format('%s: %s%% funded discount', chosen.name, chosen.discount_pct),
      format('history: %s fulfilled orders, %s paise lifetime spend', order_count, spend_paise)
    )
  );
end;
$$;

revoke execute on function public.campaign_for_account(uuid) from public, anon, authenticated;
grant execute on function public.campaign_for_account(uuid) to service_role;
