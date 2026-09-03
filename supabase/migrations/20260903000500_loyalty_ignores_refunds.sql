-- Stops a loyalty tier being farmable. The tier was counted from orders that
-- were fulfilled or refunded, so a buyer could reach 'loyal' by purchasing and
-- cancelling in a loop: the order stayed in the history, the money came back,
-- and the next quote was priced against a tier nobody had paid for. The tier is
-- not decoration. fulfill_wallet_order reads it as the funded discount a buyer
-- is already entitled to, and that entitlement is what sets the floor a price
-- may settle to, so a farmed tier lowers a real bound in the money path.
--
-- Only money that stayed counts now, which is the rule public.product_trading
-- already applies to the selling rate it reports.

create or replace function public.campaign_for_account(p_account_id uuid)
returns jsonb
language plpgsql
security invoker
set search_path = public, pg_temp
as $$
declare
  order_count integer := 0;
  spend_paise bigint := 0;
  chosen public.campaigns%rowtype;
begin
  select count(*), coalesce(sum(amount_paise), 0)
    into order_count, spend_paise
  from public.orders
  where account_id = p_account_id
    and status = 'fulfilled_via_wallet';

  select * into chosen
  from public.campaigns
  where active
    and starts_at <= now()
    and (ends_at is null or ends_at > now())
    and min_orders <= order_count
    and min_spend_paise <= spend_paise
  order by priority desc, discount_pct desc
  limit 1;

  if not found then
    return jsonb_build_object(
      'tier', 'standard',
      'discount_pct', 0,
      'orders', order_count,
      'spend_paise', spend_paise,
      'notes', jsonb_build_array('no campaign matched this buyer''s history')
    );
  end if;

  return jsonb_build_object(
    'tier', chosen.tier,
    'discount_pct', chosen.discount_pct,
    'campaign', chosen.name,
    'orders', order_count,
    'spend_paise', spend_paise,
    'notes', jsonb_build_array(
      format('%s: %s%% funded discount', chosen.name, chosen.discount_pct),
      format('history: %s fulfilled orders, %s paise lifetime spend', order_count, spend_paise)
    )
  );
end;
$$;

revoke execute on function public.campaign_for_account(uuid) from public, anon, authenticated;
grant execute on function public.campaign_for_account(uuid) to service_role;
