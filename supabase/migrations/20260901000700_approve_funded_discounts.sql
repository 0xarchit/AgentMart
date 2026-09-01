-- The approval path still refused a settled amount below the list total, so a
-- funded discount that also needed a person's decision failed at the database
-- rather than reaching them. The rest of that ordering was removed when discounts
-- became representable; these two were missed because approval is the one money
-- path a discount reaches only when it is over the standing limit as well.
--
-- Positive amounts are still required. Only the ordering between them goes.

alter table public.human_approval_requests
  drop constraint if exists human_approval_requests_check;

do $guard$
declare
  constraint_name text;
begin
  select con.conname into constraint_name
  from pg_constraint con
  join pg_class rel on rel.oid = con.conrelid
  join pg_namespace nsp on nsp.oid = rel.relnamespace
  where nsp.nspname = 'public'
    and rel.relname = 'human_approval_requests'
    and con.contype = 'c'
    and pg_get_constraintdef(con.oid) ilike '%final_amount_paise%>=%base_amount_paise%';

  if constraint_name is not null then
    execute format('alter table public.human_approval_requests drop constraint %I', constraint_name);
  end if;
end
$guard$;

create or replace function public.create_human_approval(
  p_token text,
  p_account_id uuid,
  p_telegram_id bigint,
  p_product_id uuid,
  p_qty integer,
  p_base_amount_paise bigint,
  p_final_amount_paise bigint,
  p_idempotency_key text,
  p_reason text
)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare request_row public.human_approval_requests%rowtype;
begin
  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise <= 0 then
    return jsonb_build_object('approved', false, 'reason', 'invalid approval request');
  end if;
  select * into request_row from public.human_approval_requests where idempotency_key = p_idempotency_key;
  if found then
    return jsonb_build_object('approved', true, 'duplicate', true, 'approval_id', request_row.id, 'token', request_row.token, 'expires_at', request_row.expires_at);
  end if;
  insert into public.human_approval_requests(token, account_id, telegram_id, product_id, qty, base_amount_paise, final_amount_paise, idempotency_key, reason)
  values (p_token, p_account_id, p_telegram_id, p_product_id, p_qty, p_base_amount_paise, p_final_amount_paise, p_idempotency_key, p_reason)
  on conflict (idempotency_key) do nothing
  returning * into request_row;
  if not found then
    select * into request_row from public.human_approval_requests where idempotency_key = p_idempotency_key;
    return jsonb_build_object('approved', true, 'duplicate', true, 'approval_id', request_row.id, 'token', request_row.token, 'expires_at', request_row.expires_at);
  end if;
  insert into public.audit_log(account_id, actor, action, reason, payload)
  values (p_account_id, 'gate', 'human_approval_requested', p_reason, jsonb_build_object('approval_id', request_row.id, 'token', request_row.token, 'final_amount_paise', p_final_amount_paise));
  return jsonb_build_object('approved', true, 'approval_id', request_row.id, 'token', request_row.token, 'expires_at', request_row.expires_at);
end;
$$;
