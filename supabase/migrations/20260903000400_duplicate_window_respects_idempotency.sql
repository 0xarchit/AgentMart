-- Lets the idempotency key be the duplicate check it already is. The ten minute
-- window on account and product stood beside that check and refused purchases
-- that were not duplicates at all: a second unit bought a minute after the first,
-- a gift bought after buying the same thing for yourself, a fresh attempt after
-- an order failed for an unrelated reason.
--
-- Every purchase key is derived from a stable identity, either the message that
-- asked for the purchase or the negotiation session that priced it. A retry of one
-- request therefore carries the same key and is caught by the ledger lookup below,
-- and a genuinely new request carries a new key because it is a genuinely new
-- purchase. Runaway repetition is bounded by the wallet balance, the spend limit
-- and the human approval rail, which is where a bound on spending belongs, rather
-- than by a clock that cannot tell the two apart.
--
-- Nothing else changes. The discount bound added in 20260903000300 is carried
-- forward because this replaces the whole function.

create or replace function public.fulfill_wallet_order(
  p_account_id uuid,
  p_product_id uuid,
  p_qty integer,
  p_base_amount_paise bigint,
  p_final_amount_paise bigint,
  p_razorpay_order_id text,
  p_idempotency_key text,
  p_refund_window_minutes integer default 60,
  p_run_id text default null
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
begin
  perform pg_advisory_xact_lock(hashtextextended(p_idempotency_key, 0));

  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise <= 0 then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'invalid purchase amounts or quantity', jsonb_build_object('product_id', p_product_id, 'qty', p_qty, 'base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise), p_run_id);
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
    values (p_account_id, 'gate', 'gate_reject', 'wallet balance is insufficient', jsonb_build_object('required_paise', p_final_amount_paise, 'available_paise', account_row.wallet_balance_paise), p_run_id);
    return jsonb_build_object('approved', false, 'reason', 'wallet balance is insufficient');
  end if;

  select * into product_row
  from public.products
  where id = p_product_id
  for update;

  if not found then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'product not found', jsonb_build_object('product_id', p_product_id), p_run_id);
    return jsonb_build_object('approved', false, 'reason', 'product not found');
  end if;

  -- The discount bound. Read the buyer's campaign from their own order history,
  -- floor the entitlement at cost, and refuse anything under it. A buyer with no
  -- campaign has an entitlement of zero, which puts the minimum back at the list
  -- total and is what every anonymous caller gets.
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
      values (p_account_id, 'gate', 'gate_reject', 'discount is beyond the funded entitlement', jsonb_build_object('product_id', p_product_id, 'base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'entitlement_pct', entitlement_pct, 'minimum_allowed_paise', min_allowed_paise), p_run_id);
      return jsonb_build_object('approved', false, 'reason', 'discount is beyond the funded entitlement');
    end if;
  end if;

  if product_row.stock < p_qty then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'stock is insufficient', jsonb_build_object('product_id', p_product_id, 'requested_qty', p_qty, 'available_stock', product_row.stock), p_run_id);
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

  insert into public.merchant_revenue(order_id, product_id, base_amount_paise, final_amount_paise)
  values (order_row.id, p_product_id, p_base_amount_paise, p_final_amount_paise);

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload, run_id)
  values (p_account_id, order_row.id, 'gate', 'gate_approve', 'wallet balance, catalog, stock, discount entitlement, and idempotency key checks passed', jsonb_build_object('base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'razorpay_order_id', p_razorpay_order_id), p_run_id);

  return jsonb_build_object('approved', true, 'order_id', order_row.id, 'balance_paise', new_balance, 'status', 'fulfilled_via_wallet');
end;
$$;

revoke execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text) from public, anon, authenticated;
grant execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text) to service_role;
