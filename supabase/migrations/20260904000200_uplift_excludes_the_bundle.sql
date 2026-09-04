-- The margin figure counted a bundled partner product as merchant premium.
--
-- uplift_paise is generated as final_amount_paise - base_amount_paise, and the two
-- inputs are not measured over the same goods. The base is the named product alone,
-- set from the catalog row the buyer asked for, while the final is the settled
-- amount, which includes whatever partner product the shop attached. So a bundled
-- sale reported the partner's entire discounted price as margin earned. On the shelf
-- the published comparison uses, a trim shield at INR 2400.00 with the cream
-- attached at INR 399.20 settles at INR 2828.00: the row read INR 428.00 of uplift
-- where the real premium over everything the buyer receives is INR 28.80, the
-- handling charge. Fifteen times the truth, on the number two dashboards label as
-- margin.
--
-- The base cannot absorb the partner instead. The gate refuses a request whose base
-- is not the unit price times the quantity, and that check is what makes a stale
-- price approval forgive the clock but never the number, so the basket the premium
-- is measured against has to arrive as its own field.
--
-- The buyer's own side already measures it correctly, against the list total
-- including the partner, and has disagreed with this table since bundles existed.
-- This makes the stored trail agree with the side that was right.
--
-- Rows already recorded keep a bundled amount of zero. The figure was never written
-- down at the time and cannot be recovered from the amounts, so their uplift stays
-- exactly as it was recorded rather than being quietly restated.

alter table public.merchant_revenue
  add column if not exists bundled_paise bigint not null default 0
    check (bundled_paise >= 0);

-- A generated expression cannot be altered in place, so the column is replaced.
-- Existing rows recompute with a bundled amount of zero, which reproduces the
-- value they already hold.
alter table public.merchant_revenue drop column if exists uplift_paise;

alter table public.merchant_revenue
  add column uplift_paise bigint
    generated always as (final_amount_paise - base_amount_paise - bundled_paise) stored;

comment on column public.merchant_revenue.bundled_paise is
  'List value of goods attached to the named product, already inside final_amount_paise. Outside uplift because the buyer receives it.';

comment on column public.merchant_revenue.uplift_paise is
  'Settled amount less the list total of everything the buyer receives. Positive is uplift earned, negative is a funded discount given.';

-- The settlement call carries the bundled amount, so the fulfilment function takes
-- it and records it beside the two amounts it already writes. The old signature is
-- dropped first on purpose: create or replace with an added parameter leaves both
-- versions callable, and every nine argument call would then be ambiguous.

drop function if exists public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text);

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
begin
  perform pg_advisory_xact_lock(hashtextextended(p_idempotency_key, 0));

  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise <= 0 or bundled_amount < 0 then
    insert into public.audit_log(account_id, actor, action, reason, payload, run_id)
    values (p_account_id, 'gate', 'gate_reject', 'invalid purchase amounts or quantity', jsonb_build_object('product_id', p_product_id, 'qty', p_qty, 'base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'bundled_paise', bundled_amount), p_run_id);
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

  insert into public.merchant_revenue(order_id, product_id, base_amount_paise, final_amount_paise, bundled_paise)
  values (order_row.id, p_product_id, p_base_amount_paise, p_final_amount_paise, bundled_amount);

  insert into public.audit_log(account_id, order_id, actor, action, reason, payload, run_id)
  values (p_account_id, order_row.id, 'gate', 'gate_approve', 'wallet balance, catalog, stock, discount entitlement, and idempotency key checks passed', jsonb_build_object('base_amount_paise', p_base_amount_paise, 'final_amount_paise', p_final_amount_paise, 'bundled_paise', bundled_amount, 'razorpay_order_id', p_razorpay_order_id), p_run_id);

  return jsonb_build_object('approved', true, 'order_id', order_row.id, 'balance_paise', new_balance, 'status', 'fulfilled_via_wallet');
end;
$$;

revoke execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text, bigint) from public, anon, authenticated;
grant execute on function public.fulfill_wallet_order(uuid, uuid, integer, bigint, bigint, text, text, integer, text, bigint) to service_role;

-- The approval rail has to carry the figure too. A bundled purchase over the
-- standing limit is settled from the stored approval row rather than from the run
-- that priced it, so without a column here that sale records a bundled amount of
-- zero and reports the partner as margin again. The row already locks the two
-- amounts the person is authorising, for the same reason: what settles has to be
-- what they saw.
--
-- Nothing about the decision changes. The bundled amount is not money in its own
-- right, only a statement of which part of the settled amount is goods attached to
-- the named product.

alter table public.human_approval_requests
  add column if not exists bundled_paise bigint not null default 0
    check (bundled_paise >= 0);

comment on column public.human_approval_requests.bundled_paise is
  'List value of goods attached to the named product, already inside final_amount_paise. Carried so a settlement built from this row records the same basket the run priced.';

drop function if exists public.create_human_approval(text, uuid, bigint, uuid, integer, bigint, bigint, text, text);

create or replace function public.create_human_approval(
  p_token text,
  p_account_id uuid,
  p_telegram_id bigint,
  p_product_id uuid,
  p_qty integer,
  p_base_amount_paise bigint,
  p_final_amount_paise bigint,
  p_idempotency_key text,
  p_reason text,
  p_bundled_paise bigint default 0
)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare
  request_row public.human_approval_requests%rowtype;
  bundled_amount bigint := coalesce(p_bundled_paise, 0);
begin
  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise <= 0 or bundled_amount < 0 then
    return jsonb_build_object('approved', false, 'reason', 'invalid approval request');
  end if;
  select * into request_row from public.human_approval_requests where idempotency_key = p_idempotency_key;
  if found then
    return jsonb_build_object('approved', true, 'duplicate', true, 'approval_id', request_row.id, 'token', request_row.token, 'expires_at', request_row.expires_at);
  end if;
  insert into public.human_approval_requests(token, account_id, telegram_id, product_id, qty, base_amount_paise, final_amount_paise, bundled_paise, idempotency_key, reason)
  values (p_token, p_account_id, p_telegram_id, p_product_id, p_qty, p_base_amount_paise, p_final_amount_paise, bundled_amount, p_idempotency_key, p_reason)
  on conflict (idempotency_key) do nothing
  returning * into request_row;
  if not found then
    select * into request_row from public.human_approval_requests where idempotency_key = p_idempotency_key;
    return jsonb_build_object('approved', true, 'duplicate', true, 'approval_id', request_row.id, 'token', request_row.token, 'expires_at', request_row.expires_at);
  end if;
  insert into public.audit_log(account_id, actor, action, reason, payload)
  values (p_account_id, 'gate', 'human_approval_requested', p_reason, jsonb_build_object('approval_id', request_row.id, 'token', request_row.token, 'final_amount_paise', p_final_amount_paise, 'bundled_paise', bundled_amount));
  return jsonb_build_object('approved', true, 'approval_id', request_row.id, 'token', request_row.token, 'expires_at', request_row.expires_at);
end;
$$;

revoke execute on function public.create_human_approval(text, uuid, bigint, uuid, integer, bigint, bigint, text, text, bigint) from public, anon, authenticated;
grant execute on function public.create_human_approval(text, uuid, bigint, uuid, integer, bigint, bigint, text, text, bigint) to service_role;

-- Resolution returns what the settlement rebuilds the purchase from, so the bundled
-- amount goes back out with the two amounts. Same signature, so the existing grants
-- carry over; they are restated anyway because the two functions above lost theirs
-- when they were dropped, and a reader should not have to work out which kept them.

create or replace function public.resolve_human_approval(
  p_token text,
  p_telegram_id bigint,
  p_decision text
)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare request_row public.human_approval_requests%rowtype;
begin
  select * into request_row
  from public.human_approval_requests
  where token = p_token and telegram_id = p_telegram_id
  for update;
  if not found then
    return jsonb_build_object('resolved', false, 'reason', 'approval request not found');
  end if;
  if request_row.status <> 'pending' then
    return jsonb_build_object('resolved', false, 'reason', 'approval request already resolved');
  end if;
  if request_row.expires_at <= now() then
    update public.human_approval_requests set status = 'expired', resolved_at = now() where id = request_row.id;
    insert into public.audit_log(account_id, actor, action, reason, payload)
    values (request_row.account_id, 'user', 'human_approval_expired', 'approval request expired', jsonb_build_object('approval_id', request_row.id));
    return jsonb_build_object('resolved', false, 'reason', 'approval request expired');
  end if;
  if p_decision not in ('approve', 'reject') then
    return jsonb_build_object('resolved', false, 'reason', 'invalid approval decision');
  end if;
  update public.human_approval_requests
  set status = case when p_decision = 'approve' then 'approved' else 'rejected' end, resolved_at = now()
  where id = request_row.id;
  insert into public.audit_log(account_id, actor, action, reason, payload)
  values (request_row.account_id, 'user', 'human_approval_' || p_decision, 'Telegram human decision', jsonb_build_object('approval_id', request_row.id, 'final_amount_paise', request_row.final_amount_paise));
  return jsonb_build_object(
    'resolved', true,
    'approved', p_decision = 'approve',
    'account_id', request_row.account_id,
    'product_id', request_row.product_id,
    'qty', request_row.qty,
    'base_amount_paise', request_row.base_amount_paise,
    'final_amount_paise', request_row.final_amount_paise,
    'bundled_paise', request_row.bundled_paise,
    'idempotency_key', request_row.idempotency_key
  );
end;
$$;

revoke execute on function public.resolve_human_approval(text, bigint, text) from public, anon, authenticated;
grant execute on function public.resolve_human_approval(text, bigint, text) to service_role;
