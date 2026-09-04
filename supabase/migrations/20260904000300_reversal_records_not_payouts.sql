-- A cancellation returned the same money twice.
--
-- refund_wallet_order credits the allowance, writes a purchase_refund ledger row
-- and restocks. The buyer then reversed the captured top-ups that funded the
-- allowance at the gateway, and nothing debited the allowance afterwards: the
-- balance is written in exactly three places, top-up plus, fulfil minus, refund
-- plus, and wallet_ledger.entry_type has no fourth value to express a reversal
-- debit with. Fund 5000, buy 2000, cancel: the balance goes back to 5000, the goods
-- are restocked, and 2000 goes back to the card. The shop then holds 3000 of real
-- money against 5000 of spendable allowance, and that gap settles as an ordinary
-- wallet order on the next purchase, goods shipped for money already returned.
--
-- The leg was reasoned about as evidence and implemented as a second payout. It is
-- now evidence: the reversal records the cancellation as an unpaid gateway order,
-- the same object every allowance purchase already records itself with, and moves
-- no money. The allowance is the one channel this product has, money enters it as a
-- captured top-up and leaves it as goods, and nothing anywhere pays a person out.
-- The credit had already returned the amount to where they can spend it, which is
-- also exactly what they are told: "Refund approved via wallet".
--
-- Nothing about the schema changes, which is why this migration only restates what
-- three things hold. The alternative, keeping both legs and debiting the allowance
-- by what the gateway confirmed, would have needed a fourth entry type and an RPC,
-- and it can lose money: the credit lands first, a gateway failure makes the debit
-- a later resumed attempt, and in that window the credited amount can be spent
-- beyond recovery.

comment on column public.orders.razorpay_refund_ids is
  'Gateway objects recording this cancellation. Unpaid order ids, not refund ids: the money was returned inside the wallet allowance and the gateway record is evidence of that rather than a second payout.';

comment on table public.reversal_attempts is
  'What a gateway reversal record needs to be reproduced after an interruption: the amount credited, and the key, reason and run that went into the first attempt. Progress is never stored here.';

comment on column public.reversal_attempts.run_id is
  'The run whose id went into the first attempt gateway notes, kept so a resumed record describes the conversation that cancelled the order rather than the one replaying it.';
