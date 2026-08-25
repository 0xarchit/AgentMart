-- Fix signup account provisioning: accounts.email is NOT NULL, the previous
-- trigger omitted it, so every signup failed with "Database error saving new user".

create or replace function public.create_account_for_new_user()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  insert into public.accounts (id, email, wallet_balance_paise, spend_limit_paise)
  values (new.id, new.email, 0, 250000)
  on conflict (id) do nothing;

  return new;
end;
$$;

revoke execute on function public.create_account_for_new_user() from public;

drop trigger if exists create_account_after_signup on auth.users;
create trigger create_account_after_signup
after insert on auth.users
for each row execute function public.create_account_for_new_user();

-- Backfill accounts missed while the broken trigger was deployed.
insert into public.accounts (id, email, wallet_balance_paise, spend_limit_paise)
select u.id, u.email, 0, 250000
from auth.users u
where not exists (select 1 from public.accounts a where a.id = u.id)
on conflict (id) do nothing;
