-- Make catalog discovery available to logged-out storefront visitors.

drop policy if exists products_select_authenticated on public.products;

create policy products_select_public
on public.products
for select
to anon, authenticated
using (true);
