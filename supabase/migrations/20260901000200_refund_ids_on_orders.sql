-- Records the gateway reversals a cancellation produced, so an order can be
-- checked against the payment gateway and not only against the wallet ledger. An
-- array because one cancellation can draw down more than one funding payment.

alter table public.orders
  add column if not exists razorpay_refund_ids text[] not null default '{}';

comment on column public.orders.razorpay_refund_ids is
  'Gateway refund ids produced when this order was cancelled, in the order they were taken.';
