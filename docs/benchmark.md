# Negotiating merchant against a fixed price list

Run at 2026-09-03T00:29:13+05:30, 30m34s of wall clock.

Measured at commit 72d1204, which was the tip of main when the run started.
Every number here comes from that single run against that tree, not from a live
measurement, and the buyer's decision graph has been changed since, so a re-run
today will not necessarily reproduce these figures. The harness itself is not
tracked in git, so the commit pins the agents and the rails rather than the
runner.

## Methodology

The same scenario table is run twice, in the same order, against the same
seed shelf. The buyer is identical in both passes and every judgement in
both passes is made live by the agents: nothing is scripted, replayed or
mocked, and no outcome is chosen by the harness.

The passes differ in what the shop may do with the price.

First pass, negotiating: the shop quotes from the seeded shelf, prices with
its own judgement, may pair a partner product, and may concede between the
list total and its standing ask. The cost floor is not what bounds a concession
here. No buyer in this run carried a funded discount, and without one the floor
is the list total, so a concession can only give back what the opening quote
added on top of list.

Second pass, fixed price list: the same shelf with the warranty and the pairing
removed, the trust score capped at 89, and the floor raised to the printed
price. No pricing judgement is attached, so no bundle can be added and nothing
can settle under list. The neutralising is not complete. The opening quote
charges 10 basis points of the list total for every trust point above 80, and
the flattening only caps a score at 89, so the fixed price quote still carries
that handling premium: every fixed price sale here settled at INR 1813.39
against a printed price of INR 1799.00, which is INR 57.56 of premium across
the four sales. That premium is also the only room a concession had in this
pass. The flattening lifts thin stock to fifty units as well, which is the one
difference between the passes that is not about price, and nothing in the
counted scenarios turned on stock. The shop still pitches a shortlist, because
what is being compared is pricing and not discovery.

Counting rules. Revenue is the sum of settled amounts, where settled means
the buyer chose to buy. A bundle is counted only when the shelf pairs the
chosen product and the shop actually named the partner in the conversation.
An escalation is a run handed to a person. Below cost compares each settled
amount against what those goods cost in that pass. Every rate in the table is a
share of the scenarios counted, truncated to a whole percent.

No concession is present in either column. Every settled and pending amount in
this run equals the opening quote for the product that was chosen on the shelf
that pass ran against, so no price moved after it was first quoted. What
separates the two columns is the uplift the shelf earned and the partner
product only the negotiating pass could attach.

Pairing. Every figure in the comparison below counts only scenarios that
completed in both passes. The shared free provider pool cuts runs short at
different points in each pass, and summing across whatever survived would
credit one pass with a sale the other never got a chance at. Scenarios that
completed in only one pass are listed separately, with the reason, and
contribute to no total. A provider outage is not a result either way.

Value pending approval is reported on its own line and is deliberately not
added to revenue. When an ask sits outside the buyer's rails the run stops and
hands the decision to a person, and this harness never answers that prompt,
so no money moves. Counting it as revenue would claim income that was never
collected. Excluding it silently would be worse: it would score a gate that
did its job as a lost sale, and the gate is the point. Both numbers are
published so the reader can see exactly what was sold, what is waiting on a
person, and decide for themselves. A transparent pair of numbers is worth
more here than one clean number that hides which is which.

Two readers the merchant server supports were not wired into this run, and they
bound what the table can show. Without a funded discount for the buyer the floor
is the list total, so no settled amount could fall below cost in either pass:
the priced below cost row reads 0 by construction and is not evidence that the
cost floor was tested. Without trading conditions the shop can charge neither
the warranty premium nor the scarcity premium, so the only uplift open to it was
the handling premium, which is capped at 3% of list. That also puts the 30%
premium band out of reach, so the value pending a person's approval on this
table was not produced by the band: in both escalations the ask stood above the
wallet's spend limit or above its balance. The negotiating column is therefore a
floor on what this merchant can ask, not a ceiling.

Both passes reasoned against one hosted provider, the same one in each.
Answers that arrived without their required shape and had to be asked
again: 0 across both passes.

## Comparison, paired scenarios only

| Measure | Negotiating | Fixed price list |
| --- | --- | --- |
| Scenarios counted | 6 | 6 |
| Settled | 3 | 4 |
| Revenue settled | INR 7469.39 | INR 7253.56 |
| Value pending a person's approval | INR 5656.00 (2) | INR 1813.39 (1) |
| Settled plus pending | INR 13125.39 | INR 9066.95 |
| Revenue settled per scenario | INR 1244.90 | INR 1208.93 |
| Bundle attach rate | 66% (4) | 0% (0) |
| Escalation rate | 33% (2) | 16% (1) |
| Settled rate | 50% (3) | 66% (4) |
| Priced below cost | 0 | 0 |
| Excluded, cut short by the provider | 0 | 0 |
| Excluded, lost the run | 0 | 0 |
| Reasoning time | 4m18s | 4m16s |

Revenue difference on settled money alone: INR 215.83, +2%.

Difference once value waiting on a person is included: INR 4058.44, +44%.

## Scenarios excluded from the comparison

None: every scenario completed in both passes.

## Every scenario, both passes

Rows where either side reads as an outage are the excluded ones above.

| # | Scenario | Negotiating | Fixed price list |
| --- | --- | --- | --- |
| 01 | straight match in budget | buy INR 1813.39 | buy INR 1813.39 |
| 04 | combo upsell accepted | buy INR 2828.00, bundled | buy INR 1813.39 |
| 05 | combo upsell declined by the buyer | buy INR 2828.00, bundled | buy INR 1813.39 |
| 09 | nothing fits the budget | declined | declined |
| 11 | premium band asks the person | asked a person, INR 2828.00, bundled, to a person | buy INR 1813.39 |
| 20 | wallet cannot cover it | asked a person, INR 2828.00, bundled, to a person | asked a person, INR 1813.39, to a person |
