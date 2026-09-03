-- A product must cost less than it sells for. The seed said so once, in a do
-- block that counted rows where cost_paise >= price_paise and raised if any
-- existed, and that check was true of the rows present when it ran and of
-- nothing inserted afterwards.
--
-- The gate reads this column per settlement. When a final amount sits below the
-- list total it floors the funded entitlement at cost times quantity, so a row
-- whose cost meets or passes its price produces a minimum at or above the list
-- total: every discounted settlement on that product is refused, and refused
-- with 'discount is beyond the funded entitlement', which names the wrong cause.
-- The loyalty tier becomes decoration for that one product and the audit trail
-- says the buyer overreached.
--
-- Stated as a constraint instead of an assertion, so it holds for every row that
-- ever exists rather than the ones that happened to be there at seed time.
-- Strict, matching the assertion it replaces: equal cost and price leaves no
-- margin to negotiate inside.

alter table public.products
  drop constraint if exists products_cost_below_price_check;

alter table public.products
  add constraint products_cost_below_price_check
  check (cost_paise < price_paise);
