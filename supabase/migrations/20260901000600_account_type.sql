-- Gives every account a type, so who may read the operator view is a fact in the
-- database rather than a list in the environment. An empty list used to mean
-- everyone, which is the wrong direction to fail in.
--
-- The type cannot be changed from a browser session. Postgres would otherwise let
-- an account update its own row, and the one column nobody may set for themselves
-- is the one that decides what they can see. Both controls the wallet balance
-- already uses are applied here for the same reason: the column privilege is
-- revoked, and a trigger refuses the change even if a privilege is granted back
-- by accident later.
--
-- To make an account an operator, run this as the service role or the database
-- owner, which is the only way it can be done:
--   update public.accounts set account_type = 'admin' where email = 'you@example.com';

alter table public.accounts
  add column if not exists account_type text not null default 'customer';

alter table public.accounts
  drop constraint if exists accounts_account_type_check;

alter table public.accounts
  add constraint accounts_account_type_check
  check (account_type in ('customer', 'admin'));

create or replace function public.prevent_browser_account_type_change()
returns trigger
language plpgsql
security definer
set search_path = public, pg_temp
as $$
begin
  if new.account_type is distinct from old.account_type
     and current_user not in ('service_role', 'postgres') then
    raise exception 'account type changes must be made by the service role';
  end if;
  return new;
end;
$$;

drop trigger if exists accounts_account_type_guard on public.accounts;
create trigger accounts_account_type_guard
before update on public.accounts
for each row execute function public.prevent_browser_account_type_change();

revoke update(account_type) on public.accounts from anon, authenticated;

comment on column public.accounts.account_type is
  'customer or admin. Decides who may read the operator view. Settable only by the service role.';
