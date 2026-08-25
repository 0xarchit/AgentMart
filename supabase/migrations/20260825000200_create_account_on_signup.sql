create or replace function public.create_account_for_new_user()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  insert into public.accounts (id, wallet_balance_paise, spend_limit_paise)
  values (new.id, 0, 250000)
  on conflict (id) do nothing;

  return new;
end;
$$;

revoke execute on function public.create_account_for_new_user() from public;

drop trigger if exists create_account_after_signup on auth.users;
create trigger create_account_after_signup
after insert on auth.users
for each row execute function public.create_account_for_new_user();
