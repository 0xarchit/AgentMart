# Negotiating merchant against a fixed price list

Finished at 2026-09-04T02:10:16+05:30 after 30m40s of wall clock.

Measured at commit d9a1e40, which was the tip of main when the run started.
Every number here comes from that single run against that tree, not from a live
measurement, and a re-run will not necessarily reproduce these figures: the
variance section at the end shows how far one earlier run moved. The harness
itself is not tracked in git, so the commit pins the agents and the rails rather
than the runner, and the funded discount and the scenario table described below
cannot be checked against this repository.

## Methodology

The same scenario table is run twice, in the same order, against the same seed
shelf. The buyer is identical in both passes and every judgement in both passes
is made live by the agents: nothing is scripted, replayed or mocked, and no
outcome is chosen by the harness. The passes alternate scenario by scenario
rather than running one pass to completion first, so both shops meet the same
provider conditions within a couple of minutes of each other.

The passes differ in what the shop may do with the price.

First pass, negotiating: the shop quotes from the seeded shelf, prices with its
own judgement, may pair a partner product, and may concede between its floor and
its standing ask. The buyer carries a funded discount of 45%, from a stub
standing in for the campaign rows the binary reads, so the floor is whichever is
deeper: that discount off the list total, or blended cost. The discount is set
under every cost ratio on this shelf deliberately. The trimmer every counted
sale chose lists at INR 1799.00 against a cost of INR 1100.00, and 45% off list
is INR 989.45, so cost is the binding floor rather than the discount.

Second pass, fixed price list: the same shelf with the warranty and the pairing
removed, the trust score capped at 89, thin stock lifted to fifty units, and the
cost raised to the printed price. No pricing judgement is attached, so no bundle
can be added and nothing can settle under list. The same funded discount is
wired here and the raised cost absorbs it, so the deeper floor is still the
printed price. The shop still pitches a shortlist, because what is being compared
is pricing and not discovery.

The neutralising is thinner than it looks, and on this run it came to almost
nothing. Two of the three levers it removes were unreachable anyway: the warranty
premium is gated on a refund rate that only trading conditions supply and no
conditions were wired, and no counted sale ran short of stock. The trust cap only
lowers a score above 89, and the trimmer every counted sale chose scores 88, so
both passes charged it the same handling premium of INR 14.39 over the printed
price. That leaves the partner product as the only pricing power the negotiating
pass actually exercised here, and every price the two passes both settled is
identical to the paise.

## Counting rules

Revenue is the sum of settled amounts, where settled means the buyer chose to
buy. A bundle is counted only when the shelf pairs the chosen product and the
shop actually named the partner in the conversation. An escalation is a run
handed to a person. Below cost compares each settled amount against what those
goods cost in that pass. Every rate in the table is a share of the scenarios
counted, truncated to a whole percent.

No concession is present in either column. Every settled and pending amount in
this run equals the opening quote for the product that was chosen, so no price
moved after it was first quoted. The buyer's counter tool is wired and it went
unused: in both passes the buyer either took the opening quote or handed it to a
person. Whether a concession was asked for and refused is not visible in these
figures, but none was granted.

That is what makes the priced below cost row worthless as evidence. The cost
floor is only consulted when the shop answers a counter, and no counter was
answered, so the row reads 0 because nothing ever approached the floor rather
than because the clamp held. Wiring the funded discount put the floor in cost's
hands and changed no price in this run for the same reason.

Pairing. Every figure in the comparison below counts only scenarios that
completed in both passes. The shared free provider pool cuts runs short at
different points in each pass, and summing across whatever survived would credit
one pass with a sale the other never got a chance at. Scenarios that completed in
only one pass are listed separately, with the reason, and contribute to no total.
A provider outage is not a result either way.

Value pending approval is reported on its own line and is deliberately not added
to revenue. When an ask sits outside the buyer's rails the run stops and hands
the decision to a person, and this harness never answers that prompt, so no money
moves. Counting it as revenue would claim income that was never collected.
Excluding it silently would be worse: it would score a gate that did its job as a
lost sale, and the gate is the point. Both numbers are published so the reader
can see exactly what was sold, what is waiting on a person, and decide for
themselves. A transparent pair of numbers is worth more here than one clean
number that hides which is which.

One reader the merchant server supports was not wired into this run, and it
bounds what the table can show. Without trading conditions the shop can charge
neither the warranty premium nor the scarcity premium, so the only uplift open to
it was the handling premium, which is capped at 3% of list. That also puts the
30% premium band out of reach, so the value pending a person's approval on this
table was not produced by the band. In one escalation the ask of INR 2828.00
stood above a spend limit of INR 2500.00, and in the other it stood above a
balance of INR 500.00. The negotiating column is therefore a floor on what this
merchant can ask, not a ceiling.

Both passes reasoned against one hosted provider, the same one in each, through
claude-opus-5. A comma separated value there is a fallback chain and one name
means there was no fallback, so neither pass could quietly swap brains mid
comparison. Answers that arrived without their required shape and had to be asked
again: 0 across both passes.

## Comparison, paired scenarios only

| Measure | Negotiating | Fixed price list |
| --- | --- | --- |
| Scenarios counted | 5 | 5 |
| Settled | 2 | 3 |
| Revenue settled | INR 3626.78 | INR 5440.17 |
| Value pending a person's approval | INR 5656.00 (2) | INR 1813.39 (1) |
| Settled plus pending | INR 9282.78 | INR 7253.56 |
| Revenue settled per scenario | INR 725.36 | INR 1088.03 |
| Bundle attach rate | 40% (2) | 0% (0) |
| Escalation rate | 40% (2) | 20% (1) |
| Settled rate | 40% (2) | 60% (3) |
| Priced below cost | 0 | 0 |
| Excluded, cut short by the provider | 0 | 0 |
| Excluded, lost the run | 0 | 1 |
| Buyer run time | 4m38s | 4m0s |

Revenue difference on settled money alone: INR -1813.39, -33%.

Difference once value waiting on a person is included: INR 2029.22, +27%.

Neither difference is a price difference. The two columns settled at the same
price wherever they both settled, so what moves the totals is composition.
Scenario 11 buys at INR 1813.39 against the fixed list and is handed to a person
against the negotiating shop, because the partner product raised the ask to
INR 2828.00 and that crossed the buyer's own rails. The negotiating pass books
one sale fewer and one approval request more, which costs it a third of collected
revenue and gains it a quarter more total value, sitting behind a gate that did
its job.

The buyer run time row is the summed wall clock of the buyer runs themselves. The
rest of the 30m40s is the harness pausing two minutes before every run so the
shared free provider pool can recover, eleven pauses in all.

## Scenarios excluded from the comparison

| Pass | # | Scenario | Why it is excluded |
| --- | --- | --- | --- |
| negotiating | 05 | combo upsell declined by the buyer | the other pass could not complete it |
| fixed price | 05 | combo upsell declined by the buyer | the reasoning provider's hostname failed to resolve, so the shop could not answer |

The fixed price row is a dropped DNS lookup, not a defect in the shop. The
harness recognises an outage by matching the error text, and the list it matched
against did not yet cover a failed lookup when this run started, so the run was
filed as lost rather than as unavailable. That is why the excluded, lost the run
cell above reads 1, and it should be read as an outage. Nothing in the counted
figures turns on it: scenario 05 is dropped from both passes either way, because
the comparison only counts what completed on both sides.

## Every scenario, both passes

Rows where either side reads as an outage are the excluded ones above.

| # | Scenario | Negotiating | Fixed price list |
| --- | --- | --- | --- |
| 01 | straight match in budget | buy INR 1813.39 | buy INR 1813.39 |
| 04 | combo upsell accepted | buy INR 1813.39 | buy INR 1813.39 |
| 05 | combo upsell declined by the buyer | buy INR 1813.39 | provider outage |
| 09 | nothing fits the budget | declined | declined |
| 11 | premium band asks the person | asked a person, INR 2828.00, bundled | buy INR 1813.39 |
| 20 | wallet cannot cover it | asked a person, INR 2828.00, bundled | asked a person, INR 1813.39 |

## Variance

An earlier run of the same harness, forty minutes before this one, reported a
different headline: INR 6454.78 negotiating against INR 7253.56 fixed on settled
money, which is -11%, and +33% once value waiting on a person is included. That
run is not pinned to a commit, because it was launched mid session against a tree
that carried uncommitted work; the one difference that matters between the two
trees is the fix that stops a buyer's deliberate refusal being scored as a lost
run. So the swing between the runs is not a code change.

It is the pairing. Both runs counted five paired scenarios out of six, and they
dropped different ones. This run lost scenario 05, a sale in both passes, to the
DNS outage. The earlier run lost scenario 09, a decline in both passes, because
at that tree the buyer's refusal was still scored as a lost run. Trading a sale
for a decline inside a denominator of five moves every rate and every total on
the table. Outside those two exclusions exactly one scenario behaved differently
between the runs: the negotiating shop attached the partner product in scenario
05 the first time and did not the second.

Both runs agree on the shape of the result, and the shape is what is worth
reading rather than the percentage. The negotiating shop asks for more, the extra
ask crosses the buyer's rails often enough to turn a sale into an approval
request, and total value goes up while collected revenue goes down. Five paired
scenarios cannot put a reliable number on that.
