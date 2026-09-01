-- Allows a settled amount to sit below the list total, which is what a funded
-- loyalty discount is. The list total still has to be a real amount and the
-- settled amount still has to be positive; what goes is the ordering between
-- them. The merchant's cost floor is enforced where the price is set, since only
-- the merchant knows its own cost.

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

  if product_row.stock < p_qty then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'stock is insufficient', jsonb_build_object('product_id', p_product_id, 'requested_qty', p_qty, 'available_stock', product_row.stock), p_run_id);
    return jsonb_build_object('approved', false, 'reason', 'stock is insufficient');
  end if;

  if exists (
    select 1 from public.orders
    where account_id = p_account_id
      and product_id = p_product_id
      and status in ('pending', 'fulfilled_via_wallet')
      and created_at > now() - interval '10 minutes'
  ) then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'duplicate order in the short window', jsonb_build_object('product_id', p_product_id), p_run_id);
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

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload, run_id)
  values (p_account_id, order_row.id, 'gate', 'gate_approve', 'wallet balance, catalog, stock, and duplicate checks passed', jsonb_build_object('base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'razorpay_order_id', p_razorpay_order_id), p_run_id);

  return jsonb_build_object('approved', true, 'order_id', order_row.id, 'balance_paise', new_balance, 'status', 'fulfilled_via_wallet');
end;
$$;
