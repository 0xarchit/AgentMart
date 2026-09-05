-- A refund is a run too, and so is a run that broke.
--
-- run_summary described a run entirely from its agent_run row, which only the
-- shopping pass writes. A cancellation never writes one: the person taps a button,
-- the wallet is credited, and the gateway record follows. So every descriptive
-- column came back null and the deal room called a finished refund "in progress",
-- said "No request recorded" about a tap whose reason it was holding, and printed
-- "Nothing spent" directly above two rows for the same amount. A run that failed
-- before it could decide read as "in progress" forever for the same reason.
--
-- Nothing new is recorded to fix this. The refund and failure rows already carry
-- the reason, the amount and the outcome; they are read here.

create or replace view public.run_summary
with (security_invoker = true) as
select
  run_id,
  min(created_at) as started_at,
  max(created_at) as last_at,
  count(*) as events,
  -- What the person asked for. A refund was asked for by tapping Cancel, and the
  -- reason carried on that row is the whole of what they said.
  coalesce(
    max(case when action = 'agent_run' then payload ->> 'request' end),
    max(case when action = 'refund_approved' then reason end)
  ) as request,
  max(case when action = 'agent_run' then payload -> 'run' ->> 'product_name' end) as product_name,
  -- Outcomes stay in the vocabulary the shopping pass already uses: lower case,
  -- one word for what happened. An approval is the refund, because the credit is
  -- in the allowance by the time that row is written and the gateway leg after it
  -- is the record rather than the money.
  coalesce(
    max(case when action = 'agent_run' then payload -> 'run' ->> 'action' end),
    max(case when action = 'refund_approved' then 'refunded'::text end),
    max(case when action in ('refund_reject', 'refund_failed') then 'refund_refused'::text end),
    max(case when action = 'purchase_failed' then 'failed'::text end)
  ) as outcome,
  coalesce(
    max(case when action = 'agent_run' then reason end),
    max(case when action in ('refund_reversed', 'refund_reversal_failed') then reason end),
    max(case when action in ('refund_reject', 'refund_failed') then reason end),
    max(case when action = 'purchase_failed' then reason end)
  ) as outcome_reason,
  max(case
    when action = 'agent_run' and jsonb_typeof(payload -> 'run' -> 'final_amount_paise') = 'number'
      then (payload -> 'run' ->> 'final_amount_paise')::bigint
  end) as final_amount_paise,
  -- Kept apart from final_amount_paise rather than folded into it. One is money the
  -- run spent and the other is money it put back, and a reader that cannot tell them
  -- apart reports a cancellation as a purchase.
  max(case
    when action = 'refund_approved' and jsonb_typeof(payload -> 'amount_paise') = 'number'
      then (payload ->> 'amount_paise')::bigint
  end) as returned_amount_paise
from public.audit_log
where run_id is not null
group by run_id;

grant select on public.run_summary to authenticated;
