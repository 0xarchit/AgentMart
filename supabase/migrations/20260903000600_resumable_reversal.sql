-- Make an interrupted gateway reversal resumable instead of unrepeatable.
--
-- A refund credits the allowance in one transaction and then reverses the funding
-- payments at the gateway. If the process stops between the two, the credit stands
-- and the reversal never happens: a later attempt cannot repeat it, because the
-- gateway hashes the idempotency key together with the request body, and both of
-- the inputs that produced the first body are gone. The internal key is derived
-- per Telegram message, so a re-sent command changes it, and the run id sits in
-- the gateway notes and is minted fresh for every message. A resumed leg that
-- presents either of them differently is refused as a different request under a
-- used key, after the first attempt already sent money back.
--
-- So the two inputs are written down beside the credit, in the same transaction,
-- and the reason with them: it is in the notes too, and the person may word the
-- second command differently. Nothing here records progress. What a reversal has
-- already sent back is read from the gateway's own refund records, because a crash
-- between the gateway accepting a leg and this database hearing about it would
-- make any counter of ours overstate what is left to send.
--
-- Orders refunded before this migration are deliberately not backfilled. Their
-- keys and run ids are unrecoverable, and a fabricated key is worse than an
-- absent one: it would look resumable and reverse money a second time.

create table if not exists public.reversal_attempts (
  id bigint generated always as identity primary key,
  order_id uuid not null unique references public.orders(id) on delete restrict,
  account_id uuid not null references public.accounts(id) on delete restrict,
  amount_paise bigint not null check (amount_paise > 0),
  reason text not null,
  idempotency_key text not null,
  run_id uuid,
  settled_at timestamptz,
  created_at timestamptz not null default now()
);

comment on table public.reversal_attempts is 'What a gateway reversal needs to be replayed byte for byte after an interruption. Progress is never stored here; it is read from the gateway.';
comment on column public.reversal_attempts.idempotency_key is 'The internal key the first attempt used. Per Telegram message, so it cannot be rebuilt from a later command.';
comment on column public.reversal_attempts.run_id is 'The run whose id went into the first attempt gateway notes, which the gateway hashes as part of the request body.';
comment on column public.reversal_attempts.settled_at is 'Set once the gateway has answered without error. Null means the reversal is still owed.';

-- Nothing user facing reads this table, so it gets no policy: only the service
-- role, which bypasses row level security, may see it. The revoke states that
-- intent rather than leaving it resting on the absence of a policy.
alter table public.reversal_attempts enable row level security;
revoke all on table public.reversal_attempts from anon, authenticated;

-- The previous argument list has to go: a version with one extra defaulted
-- parameter is an overload, not a replacement, and a call matching both is
-- rejected as ambiguous.
drop function if exists public.refund_wallet_order(uuid, uuid, text, text);

create function public.refund_wallet_order(
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
begin
  if trim(coalesce(p_reason, '')) = '' or trim(coalesce(p_idempotency_key, '')) = '' then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'refund reason and idempotency key are required', p_run_id);
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
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'order not found for account', p_run_id);
    return jsonb_build_object('approved', false, 'reason', 'order not found for account');
  end if;

  if order_row.status <> 'fulfilled_via_wallet' then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'order is not refundable in its current state', p_run_id);
    return jsonb_build_object('approved', false, 'reason', 'order is not refundable in its current state');
  end if;

  if order_row.refund_window_expires_at <= now() then
    insert into public.audit_log(account_id, order_id, actor, action, reason, run_id)
    values (p_account_id, p_order_id, 'gate', 'refund_reject', 'refund window has expired', p_run_id);
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
  values (p_order_id, p_account_id, order_row.amount_paise, trim(p_reason), trim(p_idempotency_key), p_run_id)
  on conflict (order_id) do nothing;

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload, run_id)
  values (p_account_id, p_order_id, 'user', 'refund_approved', p_reason, jsonb_build_object('amount_paise', order_row.amount_paise), p_run_id);

  return jsonb_build_object('approved', true, 'order_id', p_order_id, 'amount_paise', order_row.amount_paise, 'balance_paise', new_balance, 'status', 'refunded_via_wallet');
end;
$$;

revoke execute on function public.refund_wallet_order(uuid, uuid, text, text, text) from public, anon, authenticated;
grant execute on function public.refund_wallet_order(uuid, uuid, text, text, text) to service_role;
