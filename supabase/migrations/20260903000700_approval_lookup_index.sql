-- The open decision a person has not answered yet is read before any shopping
-- happens, on every free text message, to stop a second run from quoting them
-- something else while the first question is still standing. That read filters
-- telegram_id and status and takes the newest row by created_at.
--
-- The only index this table carries is keyed on account_id, which that read
-- never mentions, so it falls back to a sequential scan and a sort on a table
-- that grows a row per escalation and is never pruned. Every inbound message
-- pays for it, including the ones that turn out to have nothing pending.
--
-- The shape mirrors the account_id index deliberately: the two equality columns
-- first, then the sort key, so the ordering is answered by the index rather than
-- by a sort node above it. expires_at is left out and rechecked against the one
-- row that survives, which is cheaper than carrying it in the key.

create index if not exists human_approval_telegram_status_idx
  on public.human_approval_requests(telegram_id, status, created_at desc);
