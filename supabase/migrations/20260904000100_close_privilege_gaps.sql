-- Three privilege gaps found by audit, all of the same shape: a control that was
-- written down and does not hold at the database. Each is fixed here rather than in
-- application code, because the hole is reachable by any caller holding a session
-- token and the project URL, which is to say without going through the app at all.
--
-- 1. The account type guard could not see who was asking.
--
-- prevent_browser_account_type_change was created security definer, so
-- current_user inside it is the function owner rather than the session that issued
-- the update. Applied as the database owner, the test current_user not in
-- ('service_role', 'postgres') was false for everyone and the exception was never
-- raised. The column revoke beside it does not cover the gap either: Postgres
-- cannot revoke a column privilege from a role that holds update on the whole
-- table, which anon and authenticated do by Supabase default.
--
-- The consequence was privilege escalation. accounts_update_own restricts which
-- row a session may write, not which columns, so a signed-in customer could set
-- account_type to admin on their own row, and currentIdentity then reports them as
-- an operator: the operator dashboard and the deal room both read every account
-- through the service role once that is true.
--
-- The twin guard on wallet_balance_paise is security invoker and does work, which
-- is what makes this a slip rather than a decision. One word aligns them.

create or replace function public.prevent_browser_account_type_change()
returns trigger
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
begin
  if new.account_type is distinct from old.account_type
     and current_user not in ('service_role', 'postgres') then
    raise exception 'account type changes must be made by the service role';
  end if;
  return new;
end;
$$;

-- 2. The spend ceiling was writable straight from a session.
--
-- grant update(spend_limit_paise) to authenticated let a browser session PATCH the
-- ceiling directly through the REST gateway, which skips both things that make
-- moving it safe: the maximum the route enforces, and the audit row the route
-- writes. The column's only database bound is >= 0, so any value up to bigint max
-- was accepted, and the gate reads that column on every purchase to decide whether
-- a person has to approve. Widening it silently is the one change that turns the
-- approval rail off.
--
-- Every other money or authority column on this table follows the same rule
-- already: the write goes through the service role. The route keeps working
-- because it now updates through the service role after checking the caller's own
-- session, the way the operator views do.
--
-- Table level update is revoked rather than only the column, because a column
-- revoke against a role holding table update is a no-op.

revoke update on public.accounts from anon, authenticated;

-- 3. The merchant's cost basis was readable by any holder of the publishable key.
--
-- products_select_public grants row access with using (true) and no column
-- restriction, and the caller chooses the columns on a REST read. Leaving
-- cost_paise out of our own select protected nothing: select=cost_paise returned
-- the floor for every product. That figure is the merchant's negotiating floor and
-- the basis the funded discount clamps against, so the counterparty knowing it
-- knows exactly where a concession has to stop.
--
-- The public columns are granted back explicitly. Nothing that needs the cost
-- loses it: both Go binaries and the operator dashboard read through the service
-- role, and the storefront read never asked for it.

revoke select on public.products from anon, authenticated;

grant select (
  id,
  name,
  category,
  price_paise,
  stock,
  warranty_years,
  trust_score,
  combo_with,
  combo_discount_pct,
  image_url
) on public.products to anon, authenticated;

comment on column public.products.cost_paise is
  'Merchant cost basis. Service role only: it is the floor a concession stops at.';

comment on column public.accounts.spend_limit_paise is
  'What the agents may spend without asking. Written only through the service role, which bounds it and records the change.';
