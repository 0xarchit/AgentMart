-- Persisted human approval requests for purchases above the wallet spend limit.

create table if not exists public.human_approval_requests (
  id uuid primary key default gen_random_uuid(),
  token text not null unique,
  account_id uuid not null references public.accounts(id) on delete cascade,
  telegram_id bigint not null,
  product_id uuid not null references public.products(id) on delete restrict,
  qty integer not null check (qty > 0),
  base_amount_paise bigint not null check (base_amount_paise > 0),
  final_amount_paise bigint not null check (final_amount_paise >= base_amount_paise),
  idempotency_key text not null unique,
  status text not null default 'pending' check (status in ('pending', 'approved', 'rejected', 'expired')),
  reason text not null,
  expires_at timestamptz not null default now() + interval '15 minutes',
  created_at timestamptz not null default now(),
  resolved_at timestamptz
);

create index if not exists human_approval_account_status_idx on public.human_approval_requests(account_id, status, created_at desc);

alter table public.human_approval_requests enable row level security;
create policy human_approval_select_own on public.human_approval_requests
  for select to authenticated using ((select auth.uid()) = account_id);

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
  if p_qty <= 0 or p_base_amount_paise <= 0 or p_final_amount_paise < p_base_amount_paise then
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

revoke execute on function public.create_human_approval(text, uuid, bigint, uuid, integer, bigint, bigint, text, text) from public, anon, authenticated;
grant execute on function public.create_human_approval(text, uuid, bigint, uuid, integer, bigint, bigint, text, text) to service_role;
