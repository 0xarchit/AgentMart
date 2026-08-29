-- Correlate one shopping run end to end. A run is one person's request plus
-- everything both agents did about it. Every trail row already lands in one
-- table, so correlation needs an id on those rows, not a second table.

alter table public.audit_log add column if not exists run_id uuid;

create index if not exists audit_log_run_created_idx
  on public.audit_log(run_id, created_at);

-- run_timeline is the conversation and the money as one ordered list. Amounts
-- live at different depths depending on who wrote the row, so they are lifted
-- here once and the reader never digs through the payload to chart a run.
create or replace view public.run_timeline
with (security_invoker = true) as
select
  run_id,
  created_at as at,
  actor,
  action,
  reason,
  order_id,
  case
    when jsonb_typeof(payload -> 'amount_paise') = 'number'
      then (payload ->> 'amount_paise')::bigint
    when jsonb_typeof(payload -> 'run' -> 'final_amount_paise') = 'number'
      then (payload -> 'run' ->> 'final_amount_paise')::bigint
  end as amount_paise,
  payload
from public.audit_log
where run_id is not null;

-- run_summary is one row per run for listing them. It reads only from the
-- trail, so a run that never reached an order still appears with its outcome.
create or replace view public.run_summary
with (security_invoker = true) as
select
  run_id,
  min(created_at) as started_at,
  max(created_at) as last_at,
  count(*) as events,
  max(case when action = 'agent_run' then payload ->> 'request' end) as request,
  max(case when action = 'agent_run' then payload -> 'run' ->> 'product_name' end) as product_name,
  max(case when action = 'agent_run' then payload -> 'run' ->> 'action' end) as outcome,
  max(case when action = 'agent_run' then reason end) as outcome_reason,
  max(case
    when action = 'agent_run' and jsonb_typeof(payload -> 'run' -> 'final_amount_paise') = 'number'
      then (payload -> 'run' ->> 'final_amount_paise')::bigint
  end) as final_amount_paise
from public.audit_log
where run_id is not null
group by run_id;

grant select on public.run_timeline to authenticated;
grant select on public.run_summary to authenticated;
