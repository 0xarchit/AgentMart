-- Lets recorded revenue sit below the list total, which is what a funded loyalty
-- discount produces. The uplift column is deliberately left as it is: it computes
-- final minus base, and a negative value is the honest representation of a
-- discount rather than something to code around.
--
-- The original constraint was written inline and therefore carries a generated
-- name, so it is found by its definition rather than guessed at.

do $$
declare
  constraint_name text;
begin
  select con.conname into constraint_name
  from pg_constraint con
  join pg_class rel on rel.oid = con.conrelid
  join pg_namespace nsp on nsp.oid = rel.relnamespace
  where nsp.nspname = 'public'
    and rel.relname = 'merchant_revenue'
    and con.contype = 'c'
    and pg_get_constraintdef(con.oid) ilike '%final_amount_paise%>=%base_amount_paise%';

  if constraint_name is not null then
    execute format('alter table public.merchant_revenue drop constraint %I', constraint_name);
  end if;
end
$$;

comment on column public.merchant_revenue.uplift_paise is
  'Settled amount less the list total. Positive is uplift earned, negative is a funded discount given.';
