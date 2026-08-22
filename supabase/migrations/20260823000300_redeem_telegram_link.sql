-- Adds atomic single-use Telegram account linking for trusted services.
create or replace function public.redeem_telegram_link(
  p_token text,
  p_telegram_id bigint
)
returns uuid
language plpgsql
security definer
set search_path = public
as $$
declare
  v_link public.link_tokens%rowtype;
begin
  if nullif(trim(p_token), '') is null or p_telegram_id <= 0 then
    raise exception 'invalid link request';
  end if;

  select * into v_link
  from public.link_tokens
  where token = p_token
  for update;

  if not found then
    raise exception 'link token not found';
  end if;
  if v_link.used then
    raise exception 'link token already used';
  end if;
  if v_link.expires_at <= now() then
    raise exception 'link token expired';
  end if;

  insert into public.telegram_links (telegram_id, account_id)
  values (p_telegram_id, v_link.account_id)
  on conflict (telegram_id) do update
  set account_id = excluded.account_id,
      linked_at = now();

  update public.link_tokens
  set used = true
  where token = p_token;

  insert into public.audit_log (account_id, actor, action, reason, payload)
  values (v_link.account_id, 'user', 'telegram_linked', 'single_use_token', jsonb_build_object('telegram_id', p_telegram_id));

  return v_link.account_id;
end;
$$;

revoke execute on function public.redeem_telegram_link(text, bigint) from public, anon, authenticated;
grant execute on function public.redeem_telegram_link(text, bigint) to service_role;
