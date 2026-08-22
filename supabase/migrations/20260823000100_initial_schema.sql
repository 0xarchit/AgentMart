-- AgentMart shared schema, ownership policies, and wallet transaction functions.

create extension if not exists pgcrypto;

create table if not exists public.accounts (
  id uuid primary key references auth.users(id) on delete cascade,
  email text unique not null,
  wallet_balance_paise bigint not null default 0 check (wallet_balance_paise >= 0),
  spend_limit_paise bigint not null default 250000 check (spend_limit_paise >= 0),
  created_at timestamptz not null default now()
);

create table if not exists public.link_tokens (
  token text primary key,
  account_id uuid not null references public.accounts(id) on delete cascade,
  used boolean not null default false,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null default now() + interval '15 minutes'
);

create table if not exists public.telegram_links (
  telegram_id bigint primary key,
  account_id uuid not null references public.accounts(id) on delete cascade,
  linked_at timestamptz not null default now()
);

create table if not exists public.products (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  category text not null,
  price_paise bigint not null check (price_paise > 0),
  stock integer not null default 100 check (stock >= 0),
  warranty_years integer not null default 0 check (warranty_years >= 0),
  trust_score integer not null default 80 check (trust_score between 0 and 100),
  combo_with uuid references public.products(id),
  combo_discount_pct integer not null default 0 check (combo_discount_pct between 0 and 100)
);

create table if not exists public.orders (
  id uuid primary key default gen_random_uuid(),
  account_id uuid not null references public.accounts(id) on delete restrict,
  product_id uuid not null references public.products(id) on delete restrict,
  qty integer not null default 1 check (qty > 0),
  amount_paise bigint not null check (amount_paise > 0),
  status text not null check (status in ('pending', 'fulfilled_via_wallet', 'failed', 'refunded_via_wallet')),
  razorpay_order_id text,
  refund_window_expires_at timestamptz,
  created_at timestamptz not null default now()
);

create table if not exists public.wallet_ledger (
  id bigint generated always as identity primary key,
  account_id uuid not null references public.accounts(id) on delete restrict,
  order_id uuid references public.orders(id) on delete restrict,
  entry_type text not null check (entry_type in ('topup', 'purchase_debit', 'purchase_refund')),
  amount_paise bigint not null check (amount_paise > 0),
  balance_after_paise bigint not null check (balance_after_paise >= 0),
  razorpay_order_id text,
  razorpay_payment_id text,
  idempotency_key text not null unique,
  created_at timestamptz not null default now()
);

create table if not exists public.merchant_revenue (
  id bigint generated always as identity primary key,
  order_id uuid not null unique references public.orders(id) on delete restrict,
  product_id uuid not null references public.products(id) on delete restrict,
  base_amount_paise bigint not null check (base_amount_paise > 0),
  final_amount_paise bigint not null check (final_amount_paise > 0),
  uplift_paise bigint generated always as (final_amount_paise - base_amount_paise) stored,
  credited_at timestamptz not null default now(),
  check (final_amount_paise >= base_amount_paise)
);

create table if not exists public.audit_log (
  id bigint generated always as identity primary key,
  account_id uuid references public.accounts(id) on delete set null,
  order_id uuid references public.orders(id) on delete set null,
  actor text not null,
  action text not null,
  reason text,
  payload jsonb,
  created_at timestamptz not null default now()
);

create index if not exists audit_log_account_created_idx on public.audit_log(account_id, created_at desc);
create index if not exists orders_account_status_idx on public.orders(account_id, status);
create index if not exists wallet_ledger_account_created_idx on public.wallet_ledger(account_id, created_at desc);
create index if not exists merchant_revenue_credited_idx on public.merchant_revenue(credited_at desc);

create or replace function public.credit_wallet_topup(
  p_account_id uuid,
  p_amount_paise bigint,
  p_razorpay_order_id text,
  p_razorpay_payment_id text,
  p_idempotency_key text
)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare
  account_row public.accounts%rowtype;
  new_balance bigint;
begin
  if p_amount_paise <= 0 then
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (p_account_id, 'gate', 'wallet_topup_reject', 'top-up amount must be positive', jsonb_build_object('amount_paise', p_amount_paise));
    return jsonb_build_object('approved', false, 'reason', 'top-up amount must be positive');
  end if;

  if exists (select 1 from public.wallet_ledger where idempotency_key = p_idempotency_key) then
    return jsonb_build_object('approved', true, 'duplicate', true);
  end if;

  select * into account_row
  from public.accounts
  where id = p_account_id
  for update;

  if not found then
    return jsonb_build_object('approved', false, 'reason', 'account not found');
  end if;

  new_balance := account_row.wallet_balance_paise + p_amount_paise;

  update public.accounts
  set wallet_balance_paise = new_balance
  where id = p_account_id;

  insert into public.wallet_ledger(account_id, entry_type, amount_paise, balance_after_paise, razorpay_order_id, razorpay_payment_id, idempotency_key)
  values (p_account_id, 'topup', p_amount_paise, new_balance, p_razorpay_order_id, p_razorpay_payment_id, p_idempotency_key);

  insert into public.audit_log(account_id, actor, action, reason, payload)
  values (p_account_id, 'user', 'wallet_topup', 'verified Razorpay Checkout top-up', jsonb_build_object('amount_paise', p_amount_paise, 'razorpay_order_id', p_razorpay_order_id, 'razorpay_payment_id', p_razorpay_payment_id));

  return jsonb_build_object('approved', true, 'balance_paise', new_balance);
end;
$$;

create or replace function public.fulfill_wallet_order(
  p_account_id uuid,
  p_product_id uuid,
  p_qty integer,
  p_base_amount_paise bigint,
  p_final_amount_paise bigint,
  p_razorpay_order_id text,
  p_idempotency_key text,
  p_refund_window_minutes integer default 60
)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare
  account_row public.accounts%rowtype;
  product_row public.products%rowtype;
  order_row public.orders%rowtype;
  new_balance bigint;
  existing_order_id uuid;
begin
  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise <= 0 or p_final_amount_paise < p_base_amount_paise then
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (p_account_id, 'gate', 'gate_reject', 'invalid purchase amounts or quantity', jsonb_build_object('product_id', p_product_id, 'qty', p_qty, 'base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise));
    return jsonb_build_object('approved', false, 'reason', 'invalid purchase amounts or quantity');
  end if;

  select order_id into existing_order_id
  from public.wallet_ledger
  where idempotency_key = p_idempotency_key;

  if existing_order_id is not null then
    return jsonb_build_object('approved', true, 'duplicate', true, 'order_id', existing_order_id);
  end if;

  select * into account_row
  from public.accounts
  where id = p_account_id
  for update;

  if not found then
    return jsonb_build_object('approved', false, 'reason', 'account not found');
  end if;

  if account_row.wallet_balance_paise < p_final_amount_paise then
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (p_account_id, 'gate', 'gate_reject', 'wallet balance is insufficient', jsonb_build_object('required_paise', p_final_amount_paise, 'available_paise', account_row.wallet_balance_paise));
    return jsonb_build_object('approved', false, 'reason', 'wallet balance is insufficient');
  end if;

  select * into product_row
  from public.products
  where id = p_product_id
  for update;

  if not found then
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (p_account_id, 'gate', 'gate_reject', 'product not found', jsonb_build_object('product_id', p_product_id));
    return jsonb_build_object('approved', false, 'reason', 'product not found');
  end if;

  if product_row.stock < p_qty then
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (p_account_id, 'gate', 'gate_reject', 'stock is insufficient', jsonb_build_object('product_id', p_product_id, 'requested_qty', p_qty, 'available_stock', product_row.stock));
    return jsonb_build_object('approved', false, 'reason', 'stock is insufficient');
  end if;

  if exists (
    select 1 from public.orders
    where account_id = p_account_id
      and product_id = p_product_id
      and status in ('pending', 'fulfilled_via_wallet')
      and created_at > now() - interval '10 minutes'
  ) then
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (p_account_id, 'gate', 'gate_reject', 'duplicate order in the short window', jsonb_build_object('product_id', p_product_id));
    return jsonb_build_object('approved', false, 'reason', 'duplicate order in the short window');
  end if;

  new_balance := account_row.wallet_balance_paise - p_final_amount_paise;

  insert into public.orders(account_id, product_id, qty, amount_paise, status, razorpay_order_id, refund_window_expires_at)
  values (p_account_id, p_product_id, p_qty, p_final_amount_paise, 'pending', p_razorpay_order_id, now() + make_interval(mins => p_refund_window_minutes))
  returning * into order_row;

  update public.accounts
  set wallet_balance_paise = new_balance
  where id = p_account_id;

  insert into public.wallet_ledger(account_id, order_id, entry_type, amount_paise, balance_after_paise, razorpay_order_id, idempotency_key)
  values (p_account_id, order_row.id, 'purchase_debit', p_final_amount_paise, new_balance, p_razorpay_order_id, p_idempotency_key);

  update public.products
  set stock = stock - p_qty
  where id = p_product_id;

  update public.orders
  set status = 'fulfilled_via_wallet'
  where id = order_row.id;

  insert into public.merchant_revenue(order_id, product_id, base_amount_paise, final_amount_paise)
  values (order_row.id, p_product_id, p_base_amount_paise, p_final_amount_paise);

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload)
  values (p_account_id, order_row.id, 'gate', 'gate_approve', 'wallet balance, catalog, stock, and duplicate checks passed', jsonb_build_object('base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'razorpay_order_id', p_razorpay_order_id));

  return jsonb_build_object('approved', true, 'order_id', order_row.id, 'balance_paise', new_balance, 'status', 'fulfilled_via_wallet');
end;
$$;

create or replace function public.refund_wallet_order(
  p_account_id uuid,
  p_order_id uuid,
  p_idempotency_key text
)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare
  account_row public.accounts%rowtype;
  order_row public.orders%rowtype;
  new_balance bigint;
begin
  if exists (select 1 from public.wallet_ledger where idempotency_key = p_idempotency_key) then
    return jsonb_build_object('approved', true, 'duplicate', true, 'order_id', p_order_id);
  end if;

  select * into order_row
  from public.orders
  where id = p_order_id and account_id = p_account_id
  for update;

  if not found then
    insert into public.audit_log(account_id, order_id, actor, action, reason)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'order not found for account');
    return jsonb_build_object('approved', false, 'reason', 'order not found for account');
  end if;

  if order_row.status <> 'fulfilled_via_wallet' then
    insert into public.audit_log(account_id, order_id, actor, action, reason)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'order is not refundable in its current state');
    return jsonb_build_object('approved', false, 'reason', 'order is not refundable in its current state');
  end if;

  if order_row.refund_window_expires_at <= now() then
    insert into public.audit_log(account_id, order_id, actor, action, reason)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'refund window has expired');
    return jsonb_build_object('approved', false, 'reason', 'refund window has expired');
  end if;

  select * into account_row
  from public.accounts
  where id = p_account_id
  for update;

  new_balance := account_row.wallet_balance_paise + order_row.amount_paise;

  update public.accounts
  set wallet_balance_paise = new_balance
  where id = p_account_id;

  insert into public.wallet_ledger(account_id, order_id, entry_type, amount_paise, balance_after_paise, idempotency_key)
  values (p_account_id, p_order_id, 'purchase_refund', order_row.amount_paise, new_balance, p_idempotency_key);

  update public.orders
  set status = 'refunded_via_wallet'
  where id = p_order_id;

  update public.products
  set stock = stock + order_row.qty
  where id = order_row.product_id;

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload)
  values (p_account_id, p_order_id, 'gate', 'refund_approved', 'wallet credit issued within the refund window', jsonb_build_object('amount_paise', order_row.amount_paise));

  return jsonb_build_object('approved', true, 'order_id', p_order_id, 'balance_paise', new_balance, 'status', 'refunded_via_wallet');
end;
$$;

revoke execute on function public.credit_wallet_topup(uuid, bigint, text, text, text) from public, anon, authenticated;
revoke execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer) from public, anon, authenticated;
revoke execute on function public.refund_wallet_order(uuid, uuid, text) from public, anon, authenticated;
grant execute on function public.credit_wallet_topup(uuid, bigint, text, text, text) to service_role;
grant execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer) to service_role;
grant execute on function public.refund_wallet_order(uuid, uuid, text) to service_role;

create or replace function public.prevent_browser_wallet_balance_change()
returns trigger
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
begin
  if new.wallet_balance_paise is distinct from old.wallet_balance_paise
     and current_user not in ('service_role', 'postgres') then
    raise exception 'wallet balance changes must use the trusted transaction function';
  end if;
  return new;
end;
$$;

drop trigger if exists accounts_wallet_balance_guard on public.accounts;
create trigger accounts_wallet_balance_guard
before update on public.accounts
for each row execute function public.prevent_browser_wallet_balance_change();

revoke update(wallet_balance_paise) on public.accounts from anon, authenticated;
grant update(spend_limit_paise) on public.accounts to authenticated;

alter table public.accounts enable row level security;
alter table public.link_tokens enable row level security;
alter table public.telegram_links enable row level security;
alter table public.products enable row level security;
alter table public.orders enable row level security;
alter table public.wallet_ledger enable row level security;
alter table public.merchant_revenue enable row level security;
alter table public.audit_log enable row level security;

create policy accounts_select_own on public.accounts for select to authenticated using ((select auth.uid()) = id);
create policy accounts_insert_own on public.accounts for insert to authenticated with check ((select auth.uid()) = id);
create policy accounts_update_own on public.accounts for update to authenticated using ((select auth.uid()) = id) with check ((select auth.uid()) = id);
grant update(spend_limit_paise) on public.accounts to authenticated;

create policy link_tokens_select_own on public.link_tokens for select to authenticated using ((select auth.uid()) = account_id);
create policy link_tokens_insert_own on public.link_tokens for insert to authenticated with check ((select auth.uid()) = account_id);

create policy products_select_authenticated on public.products for select to authenticated using (true);

create policy orders_select_own on public.orders for select to authenticated using ((select auth.uid()) = account_id);
create policy wallet_ledger_select_own on public.wallet_ledger for select to authenticated using ((select auth.uid()) = account_id);
create policy audit_log_select_own on public.audit_log for select to authenticated using ((select auth.uid()) = account_id);
create policy merchant_revenue_select_own on public.merchant_revenue for select to authenticated using (
  exists (
    select 1 from public.orders
    where orders.id = merchant_revenue.order_id
      and orders.account_id = (select auth.uid())
  )
);
