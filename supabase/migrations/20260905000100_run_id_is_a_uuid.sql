-- A run id reaches the money functions as text and lands in a uuid column, and
-- Postgres refuses that at plan time rather than at value time:
--
--   42804: column "run_id" is of type uuid but expression is of type text
--
-- 20260830000100 added audit_log.run_id as uuid. Every function that carries a run
-- has declared p_run_id as text since 20260830000200, and inserts it straight into
-- that column. Text to uuid has no assignment cast in Postgres, only an explicit
-- one, so the statement fails to plan whatever the parameter holds, including null.
-- Nothing caught it: the Go tests drive a fake REST server rather than a database,
-- and a trail row written from Go goes through PostgREST, which casts per column
-- and works. Only the two plpgsql functions are affected, and both of them are on
-- the money path, so a correctly migrated database refuses every purchase and every
-- refund while every other surface looks healthy.
--
-- The value was never wrong. runid.New is uuid.NewString, so what arrives is a uuid
-- string and the column is the right type for it. What was wrong is the parameter
-- type, and the fix is to convert once on the way in rather than to weaken the
-- column.

-- A malformed run id must not cost someone their purchase. It is a correlation id,
-- not money, so anything that is not a uuid becomes null and the sale proceeds
-- untraced rather than failing. A bare cast would raise 22P02 and lose the order.
create or replace function public.run_id_uuid(p_run_id text)
returns uuid
language sql
immutable
set search_path = public, pg_temp
as $$
  select case
    when btrim(coalesce(p_run_id, '')) ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
      then btrim(p_run_id)::uuid
  end;
$$;

revoke execute on function public.run_id_uuid(text) from public, anon, authenticated;
grant execute on function public.run_id_uuid(text) to service_role;

-- The two bodies below are the current definitions with one line added to each
-- declare block and the parameter swapped for it at every trail write. Nothing else
-- about either function changes, and neither signature does, so no drop is needed
-- and the existing grants survive the replace.

create or replace function public.fulfill_wallet_order(
  p_account_id uuid,
  p_product_id uuid,
  p_qty integer,
  p_base_amount_paise bigint,
  p_final_amount_paise bigint,
  p_razorpay_order_id text,
  p_idempotency_key text,
  p_refund_window_minutes integer default 60,
  p_run_id text default null,
  p_bundled_paise bigint default 0
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
  entitlement_pct integer := 0;
  cost_floor_paise bigint := 0;
  min_allowed_paise bigint;
  bundled_amount bigint := coalesce(p_bundled_paise, 0);
  run_uuid uuid := public.run_id_uuid(p_run_id);
begin
  perform pg_advisory_xact_lock(hashtextextended(p_idempotency_key, 0));

  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise <= 0 or bundled_amount < 0 then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'invalid purchase amounts or quantity', jsonb_build_object('product_id', p_product_id, 'qty', p_qty, 'base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'bundled_paise', bundled_amount), run_uuid);
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
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'wallet balance is insufficient', jsonb_build_object('required_paise', p_final_amount_paise, 'available_paise', account_row.wallet_balance_paise), run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'wallet balance is insufficient');
  end if;

  select * into product_row
  from public.products
  where id = p_product_id
  for update;

  if not found then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'product not found', jsonb_build_object('product_id', p_product_id), run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'product not found');
  end if;

  -- The discount bound. Read the buyer's campaign from their own order history,
  -- floor the entitlement at cost, and refuse anything under it. A buyer with no
  -- campaign has an entitlement of zero, which puts the minimum back at the list
  -- total and is what every anonymous caller gets.
  --
  -- The bundled amount is deliberately not part of this arithmetic. It raises the
  -- settled amount without raising what the merchant may discount, and counting it
  -- here would let an attached partner widen the funded discount on the main
  -- product by its own price.
  if p_final_amount_paise < p_base_amount_paise then
    entitlement_pct := coalesce((public.campaign_for_account(p_account_id) ->> 'discount_pct')::integer, 0);
    if entitlement_pct < 0 or entitlement_pct >= 100 then
      entitlement_pct := 0;
    end if;
    min_allowed_paise := p_base_amount_paise - (p_base_amount_paise * entitlement_pct / 100);
    cost_floor_paise := product_row.cost_paise * p_qty;
    if cost_floor_paise > min_allowed_paise then
      min_allowed_paise := cost_floor_paise;
    end if;
    if p_final_amount_paise < min_allowed_paise then
      insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
      values (p_account_id, 'gate', 'gate_reject', 'discount is beyond the funded entitlement', jsonb_build_object('product_id', p_product_id, 'base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'entitlement_pct', entitlement_pct, 'minimum_allowed_paise', min_allowed_paise), run_uuid);
      return jsonb_build_object('approved', false, 'reason', 'discount is beyond the funded entitlement');
    end if;
  end if;

  if product_row.stock < p_qty then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'stock is insufficient', jsonb_build_object('product_id', p_product_id, 'requested_qty', p_qty, 'available_stock', product_row.stock), run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'stock is insufficient');
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

  insert into public.merchant_revenue(order_id, product_id, base_amount_paise, final_amount_paise, bundled_paise)
  values (order_row.id, p_product_id, p_base_amount_paise, p_final_amount_paise, bundled_amount);

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload, run_id)
  values (p_account_id, order_row.id, 'gate', 'gate_approve', 'wallet balance, catalog, stock, discount entitlement, and idempotency key checks passed', jsonb_build_object('base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'bundled_paise', bundled_amount, 'razorpay_order_id', p_razorpay_order_id), run_uuid);

  return jsonb_build_object('approved', true, 'order_id', order_row.id, 'balance_paise', new_balance, 'status', 'fulfilled_via_wallet');
end;
$$;

revoke execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text, bigint) from public, anon, authenticated;
grant execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text, bigint) to service_role;

create or replace function public.refund_wallet_order(
  p_account_id uuid,
  p_order_id uuid,
  p_reason text,
  p_idempotency_key text,
  p_run_id text default null
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
  run_uuid uuid := public.run_id_uuid(p_run_id);
begin
  if trim(coalesce(p_reason, '')) = '' or trim(coalesce(p_idempotency_key, '')) = '' then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'refund reason and idempotency key are required', run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'refund reason and idempotency key are required');
  end if;

  if exists (select 1 from public.wallet_ledger where idempotency_key = p_idempotency_key) then
    return jsonb_build_object('approved', true, 'duplicate', true, 'order_id', p_order_id);
  end if;

  select * into order_row
  from public.orders
  where id = p_order_id and account_id = p_account_id
  for update;

  if not found then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'order not found for account', run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'order not found for account');
  end if;

  if order_row.status <> 'fulfilled_via_wallet' then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'order is not refundable in its current state', run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'order is not refundable in its current state');
  end if;

  if order_row.refund_window_expires_at <= now() then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'refund window has expired', run_uuid);
    return jsonb_build_object('approved', false, 'reason', 'refund window has expired');
  end if;

  select * into account_row
  from public.accounts
  where id = p_account_id
  for update;

  new_balance := account_row.wallet_balance_paise + order_row.amount_paise;

  update public.accounts set wallet_balance_paise = new_balance where id = p_account_id;

  insert into public.wallet_ledger(account_id, order_id, entry_type, amount_paise, balance_after_paise, idempotency_key)
  values (p_account_id, p_order_id, 'purchase_refund', order_row.amount_paise, new_balance, p_idempotency_key);

  update public.orders set status = 'refunded_via_wallet' where id = p_order_id;
  update public.products set stock = stock + order_row.qty where id = order_row.product_id;

  -- The credit and the means of reversing it at the gateway commit together, so
  -- there is no instant where one exists without the other. A conflict is
  -- unreachable while a refunded order cannot become refundable again, and if
  -- that ever changes a bookkeeping duplicate must not fail a credit.
  insert into public.reversal_attempts(order_id, account_id, amount_paise, reason, idempotency_key, run_id)
  values (p_order_id, p_account_id, order_row.amount_paise, trim(p_reason), trim(p_idempotency_key), run_uuid)
  on conflict (order_id) do nothing;

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload, run_id)
  values (p_account_id, p_order_id, 'user', 'refund_approved', p_reason, jsonb_build_object('amount_paise', order_row.amount_paise), run_uuid);

  return jsonb_build_object('approved', true, 'order_id', p_order_id, 'amount_paise', order_row.amount_paise, 'balance_paise', new_balance, 'status', 'refunded_via_wallet');
end;
$$;

revoke execute on function public.refund_wallet_order(uuid, uuid, text, text, text) from public, anon, authenticated;
grant execute on function public.refund_wallet_order(uuid, uuid, text, text, text) to service_role;
