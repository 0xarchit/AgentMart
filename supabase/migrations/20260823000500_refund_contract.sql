-- Correct the wallet refund contract and audit the human-provided reason.

drop function if exists public.refund_wallet_order(uuid, uuid, text);

create function public.refund_wallet_order(
  p_account_id uuid,
  p_order_id uuid,
  p_reason text,
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
  if trim(coalesce(p_reason, '')) = '' or trim(coalesce(p_idempotency_key, '')) = '' then
    insert into public.audit_log(account_id, order_id, actor, action, reason)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'refund reason and idempotency key are required');
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

  update public.accounts set wallet_balance_paise = new_balance where id = p_account_id;

  insert into public.wallet_ledger(account_id, order_id, entry_type, amount_paise, balance_after_paise, idempotency_key)
  values (p_account_id, p_order_id, 'purchase_refund', order_row.amount_paise, new_balance, p_idempotency_key);

  update public.orders set status = 'refunded_via_wallet' where id = p_order_id;
  update public.products set stock = stock + order_row.qty where id = order_row.product_id;

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload)
  values (p_account_id, p_order_id, 'user', 'refund_approved', p_reason, jsonb_build_object('amount_paise', order_row.amount_paise));

  return jsonb_build_object('approved', true, 'order_id', p_order_id, 'amount_paise', order_row.amount_paise, 'balance_paise', new_balance, 'status', 'refunded_via_wallet');
end;
$$;

revoke execute on function public.refund_wallet_order(uuid, uuid, text, text) from public, anon, authenticated;
grant execute on function public.refund_wallet_order(uuid, uuid, text, text) to service_role;
