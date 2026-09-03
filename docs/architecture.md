# AgentMart agentic architecture

Design first, then implementation. This is the contract the code follows.

Two rules govern everything below.

1. Every decision is an agent's. Nothing in code decides what to buy, what to
   offer, what to counter, or when to involve a person.
2. Every money movement is bounded by code. The cost floor, the wallet, the
   spend limit, the stock check, and the atomic ledger write are not
   negotiable and are not model-generated.

The result of the two together: a conversation that reads like two people
trading, and a money trail that cannot be talked into anything.

---

## 1. Complete flow

One purchase, end to end. Every arrow is a real call.

```text
person  "buy me a good trimmer"
  |
  v
BUYER AGENT
  1. brief          reads the request and the person's money facts
  |
  v  "show me trimmers, around 3000"                      (agent to agent)
MERCHANT AGENT
  2. shopfront      searches its own stock, pitches 2 to 4 options like an owner
  |                 prices come from catalog rows, never from the model
  v  greeting + options + any campaign it wants to lead with
BUYER AGENT
  3. choose         picks one from the pitch, says why
  |
  v  "firm price on the Titan XL, one unit"
MERCHANT AGENT
  4. quote          opening offer: uplift, bundle, or loyalty deal
  |                 fixed arithmetic on catalog rows, never below the list total
  v  amount + reason + session id
BUYER AGENT
  5. judge          accept / negotiate / ask_human / decline
  |                 money guard may only downgrade an unaffordable accept
  |                 to ask_human. It never converts a negotiate or a decline.
  +--- negotiate -> 6. haggle loop (below)
  +--- accept ------> 7. settle
  +--- ask_human ---> 8. hand to the person
  +--- decline -----> 9. close the session
```

### 6. The haggle loop

```text
buyer haggle agent                     merchant strategist
  counter 2450 "seen it cheaper"  ->   hold | concede | bundle_sweetener
                                        | loyalty_discount
                                  <-   2560 "can include the oil at 2560"
  counter 2500 "meet me here"      ->   concede
                                  <-   2500 accepted
```

Both sides are agents. The loop ends when the merchant accepts, the buyer
accepts, the buyer walks, or the server's round cap is hit. Every round
appends two turns to the session transcript.

The strategist and its price guard act here and nowhere else. Step 4's opening
offer is fixed arithmetic over catalog rows: it can charge an uplift, attach the
partner the shelf pairs, and apply a funded loyalty discount, and it can never
open below the list total. A model is asked for a price only once a buyer has
countered, and what it answers is clamped to the rails and written to the trail
before it is sent.

### 7 to 9. Settlement

- **accept**: the buyer agent accepted, so the purchase goes through the gate.
  Wallet balance, spend limit, stock, and price freshness are checked, then one
  atomic ledger write moves the money and returns a receipt.
- **ask_human**: an approval row is written with a token, and the person gets
  the amount, the reasoning, the buttons, and the transcript. No money moves.
  The negotiation session stays open, because the deal is genuinely pending a
  person. On approval the purchase resumes through the same gate. A standing
  question holds free text only, because starting a fresh run would abandon the
  question it was asked about. An explicit `/buy` or `/accept` still runs: each
  names the purchase it means, so it is a new instruction rather than an answer
  to the open one, and it reaches the same gate. Above the standing limit it
  raises its own question instead of spending, and the balance is re-read on
  every attempt, so two open questions cannot overdraw the allowance between
  them.
- **decline**: the session is closed with a reason. Nothing moves.

### What gets recorded

| Artifact | Written by | Contains |
| --- | --- | --- |
| session transcript | negotiation server | every turn from both agents, browse through settlement |
| `agent_run` | buyer | the decision, the reasoning, the amounts, and the conversation, before money moves |
| `offer_priced` | merchant | every strategist counter with its strategy, rails, guard note and margin |
| gate decision | gate layer | approved or refused, with the reason |
| wallet ledger + receipt | wallet layer | the atomic money movement, returning the order id |
| merchant revenue | fulfilment function | base, final, and the generated uplift per order |
| exported `.txt` | user agent | the conversation as the person can read it |

Audit is fail closed on both sides. An offer that cannot be explained is not
sent. A purchase that cannot be recorded does not happen.

The opening quote has no `offer_priced` row of its own, because there is no
choice to record: the same shelf and the same quantity always produce the same
number. What explains it is the session transcript, which holds the amount and
the shop's own words for it, and the buyer's `agent_run`, which holds the amount
it acted on. That matters more than it sounds, because a buyer that takes the
opening quote never reaches the strategist at all, and on the published
comparison every sale did exactly that.

Every row above carries a run id, generated when the person sends the message
and carried to the shop on each negotiation message, so both sides write into
one story. Two views read it back: `run_timeline` for one run in order, and
`run_summary` for one row per run.

---

## 2. Agents

Five agents. Each one owns a judgement and nothing else.

### Buyer side

The person's sentence is not preprocessed. It travels to the shop as written,
because any extraction step is code deciding what the person meant.

| Agent | Sees | Decides | Answers with |
| --- | --- | --- | --- |
| choose | the shop's pitched options | which one, how many, why | product id, quantity, reason |
| judge | the offer plus the money facts | accept, negotiate, ask_human, decline | decision, reason |
| haggle | the offer, the rounds left, the tools | what to counter and when to stop | outcome, transcript |

### Merchant side

| Agent | Sees | Decides | Answers with |
| --- | --- | --- | --- |
| shopfront | the brief, the budget, its own stock, the buyer's campaign tier | what to show and how to sell it | greeting, options with pitches, closing |
| strategist | the session, the buyer's counter, the floor, campaign budget | hold, concede, bundle sweetener, loyalty discount | strategy, amount, reason |

### The guards, which are not agents

| Guard | Rule | On violation |
| --- | --- | --- |
| price truth | option prices come from catalog rows | model-invented ids and prices are dropped |
| price guard | no amount below the cost floor, none above the ask | clamped, with the clamp recorded |
| money guard | an accept the wallet or budget cannot fund | routed to the person, never to a silent refusal |
| gate | balance, spend limit, stock, price freshness | refused with a reason |
| audit | every offer and every purchase is recorded first | the action is refused |

---

## 3. Graphs

### Buyer graph

```text
START
  |
  v
ask_shop (fn: agent to agent browse, carrying the person's words unchanged)
  |
  v
choose (agent, run inline so the browse turns keep travelling)
  |
  v
quote (fn: agent to agent propose)
  |
  v
decide (fn: runs the judging agent, applies the money guard, emits a route)
  |
  +-- ACCEPT ----> accept (fn)
  +-- NEGOTIATE -> haggle (agent with tools)
  +-- ASK_HUMAN -> hand_over (fn)
  +-- DECLINE ---> close (fn)
        |
        v
      join
        |
        v
     finalize (fn: re-verify the product, carry the transcript out)
```

Three structural rules learned the hard way:

- **Everything a node needs arrives as its input.** No node reads state a
  previous node left on a shared object. That mistake produced a run that
  fetched an offer and then failed with "no merchant offer in flight".
- **A judgement and the thing judged stay in one scope.** Where an agent's
  answer must be paired with facts the agent must not restate, the node runs
  that agent inline rather than receiving its output over an edge.
- **Once a quote is in hand, nothing downstream loses the run.** A failure after
  the offer exists hands that offer to the person with the reason. Escalation
  never spends, so this stays money safe.

### Merchant graph

```text
browse:     find_candidates (fn) -> shopfront (agent) -> price_truth (fn)
negotiate:  campaign_eligibility (fn) -> strategist (agent) -> price_guard (fn) -> audit (fn)
```

---

## 4. Reasoning layer

Every agent talks to the provider over `/chat/completions`, because that is the
one wire format every gateway in this project accepts.

Two things are mandatory per call and were both missing before:

1. **The instruction must be sent.** It arrives on the request config, not in
   the conversation. Dropping it leaves the model with no role and no output
   contract, and it answers the raw user text as an open-ended chat.
2. **The answer must be shaped.** Each node declares a schema derived from its
   own Go result type, and the request declares that schema as a function the
   model answers through. It is forced when the node has no other tools, and
   offered alongside them when it has.

No scripted fallback on the shipped decision path. When a provider fails inside
the buyer graph or the merchant graph, the error surfaces. A demo that silently
degrades to `if` statements is not an agentic demo.

One exception is deliberate and named here rather than left to a reader's grep:
with no merchant model configured, `marketgraph.New` returns no negotiator and
each round is priced by the orchestrator's own concession schedule
(`concedeSchedule` in `internal/negotiation/orchestrator.go`) under the same cost
floor, so a merchant with no provider key still negotiates and still cannot sell
below cost.

---

## 5. Money surface

What exists today, stated without flattery.

| Step | Mechanism | Payment object |
| --- | --- | --- |
| funding the allowance | hosted checkout, verified from the webhook and from the browser callback alike, credited once by key | a real captured payment |
| agent purchase | order artifact created, then an atomic wallet debit | an order, no payment |
| human approval | approval row plus a token, resumed through the gate, settled through a swappable adapter | none yet |
| cancellation | wallet credit against the order id | none |

The allowance model is the right shape for a delegated agent: the person funds a
bounded balance with a spend limit, and the agent operates inside it. The gap is
that only the funding step produces a payment object, so two of the four money
moments have nothing an outside party can verify.

What has been built toward closing it. Settlement is now one interface behind the
gate, `internal/buyer/settlement.go`, swapped at a single call, so what moves the
money can change without touching a single bound the gate applies. A charge
creating tool is kept out of every toolset a reasoning layer can see, and that
refusal is a test rather than a paragraph.

What is built and not wired, stated separately because the distinction matters.
The category is empty now, and the heading stays because both things that were in
it left by being reached rather than by being reclassified.
`internal/razorpay/sales.go`, a read-only view of gateway sales, structurally
read-only in that the file contains no write verb, was described as shipped in
this document until it was pointed out that shipped has to mean a stranger can
reach it: it was a package, not a feature, until something called it.
`cmd/market/main.go:68` now hands the gateway to the trading provider, which reads
the view per five minute window at `trading/trading.go:112` and feeds its refund
rate into the conditions the shop prices from. `verifyCheckoutSignature` in
`web/lib/razorpay.ts` left for the same reason: the browser callback now posts to
`web/app/api/topups/confirm/route.ts`, which verifies the signature and credits
the wallet under the same idempotency key the webhook uses, so the two paths race
harmlessly and a lost webhook no longer strands the money.

What was rejected, and why it matters. A payment link the person pays was the
obvious way to give the approval path a payment object, and it was turned down:
it puts a human in the per purchase loop, which is the one thing this design is
about not doing. The correct primitive is a mandate granted once and drawn
against by the agent, and whether that primitive is actually reachable was
settled by probing the live gateway rather than by reading the product pages.

It is not reachable, and the reason is an account level feature grant rather
than anything about test mode. Three findings say so independently. The one
route that charges a mandate with nobody present,
`POST /v1/payments/create/recurring`, answers a bad request error reading "The
requested URL was not found on the server." with its source given as internal,
on four request shapes including an empty body, so it refuses before it reads a
payload at all; a path that genuinely does not exist is refused earlier and
differently, by the edge, with "no Route matched with those values".
Subscriptions and plans, the other way to charge on a schedule, answer
"Unauthorized" on the live secret while a deliberately wrong secret answers
"Authentication failed" on the same call, and ten other endpoints answered
normally on that same credential in the same session: two different refusals for
a good key and a bad one mean the key is fine and the feature is shut. Both
documentation trees then say so in prose, that this is granted on request
through their support team, and no sandbox override exists.

What makes it worth writing down is how convincingly the first half works. A
registration validates the frequency and the maximum amount, returns a real
NACH mandate form naming the bank, and hands back a working authorisation link.
The token echoed back with it has no id, its recurring status is null, and
listing that customer's tokens returns none. Registration is open and charging
is closed, so a build that stopped at a successful registration would have
reported a working mandate and been wrong.

The alternative that also needs no person, authorising a payment and capturing
it later, was investigated in the same round and deliberately not adopted. The
amount is fixed when the authorisation is taken, and the whole point here is an
agent that arrives at an amount by negotiating it. A mechanism that wants the
number before the conversation contradicts the premise, so it is recorded as
examined and rejected rather than built. The position is therefore that the
adapter is ready and the capability is withheld, which stays a one file swap and
not a redesign.

## 6. Known defects in the contract

| Defect | Where | Effect |
| --- | --- | --- |
| bundled goods not carried through the settling agent | `shopgraph/builder.go` negotiate branch | a negotiated bundle is still measured against the main product alone |
| the uplift bounds are chosen, not measured | `negotiation/conditions.go` | the amounts inside them are argued from observations, but the ceilings are judgement |
| no drawable mandate on this account | `internal/buyer/settlement.go` | the approval path settles from the allowance, not from a payment object, and the route that would charge a mandate is withheld per account rather than by test mode |
| the buyer's account id is self asserted | `negotiation/http.go`, one shared token for all callers | a caller can claim another account's loyalty tier and write trail rows against it |
| cost is enforced on the merchant side only | `internal/gate` has no cost knowledge | "never below cost" is proven where the price is set, not at both ends |
| the gateway sales view reads one page | `internal/razorpay/sales.go:41`, `count=100` with no paging | the refund rate behind an opening quote is computed from the first hundred payments and refunds in the window rather than from all of them |

Every row above was verified against the code, with a file and a line, rather
than carried over from notes. The list is deliberately complete: a defect that is
written down can be scheduled or accepted on purpose, and one that is not gets
found by someone else.

Closed since this list was written, and kept here rather than deleted, because a
defect that was found and fixed is evidence about how this was built and a defect
quietly removed from a list is not. Each row names what proves the fix instead of
asserting it.

| Was | Closed by | Proved by |
| --- | --- | --- |
| one shared negotiation slot for all callers | the in flight input is keyed by the graph pass that stored it, `marketgraph/nodes.go:275`, and deleted on the way out | structure rather than a test: `nodes.go:38-42` is a `sync.Map` keyed per pass, so there is no shared cell left to cross |
| the price freshness rail could not fire | the instant a quote was observed travels with the request instead of being read as now, `buyer/purchase.go:174` | `stale_price_test.go:39` refuses a quote older than the window, `:49` still buys inside it, and `:56` treats no observation as priced now |
| thirteen money path refusals returning with no trail row, where this list said three, including the amount integrity refusal that fires before the gate is consulted | one recorder per boundary that every refusal leaves through, `buyer/purchase.go:129` and `buyer/refund.go:80` | `purchase_trail_test.go` and `refund_test.go`, which fail both on a missing row and on a doubled one |
| the concession schedule was prose, not code | this round's concession floor is one of the bounds `clampToRails` chooses between, `marketgraph/graph.go:133` | `marketgraph_test.go:39`, which fails if the round's floor is only advisory |
| the checkout signature verifier had no caller | `web/app/api/topups/confirm/route.ts` credits the wallet from the browser callback, under the idempotency key the webhook already uses | `razorpay.test.ts` covers the four facts that decide the credit, including a captured payment replayed by an account it was not opened for |
| a tool returned its own argument | `get_current_terms` is removed rather than implemented, since a tool that hands back its own argument is worse than a missing one | no toolset registers the name and no code refers to it |
| a ten minute window on account and product refused purchases that were not duplicates: a second unit, a gift, a fresh attempt after an unrelated failure | the idempotency key is left to be the duplicate check it already is, migration `20260903000400` | every purchase key is derived from the message or the negotiation session that asked for it, so a retry carries the same key and a genuinely new purchase carries a new one; runaway repetition is bounded by the balance, the spend limit and the approval rail instead of by a clock |
| a loyalty tier was farmable by buying and refunding in a loop, and the tier is not decoration: it is the entitlement that sets the floor a price may settle to | the tier counts only the orders whose money stayed, migration `20260903000500` | structure rather than a test, since nothing here runs SQL: `refund_wallet_order` moves a reversed order to `refunded_via_wallet` (`20260823000500_refund_contract.sql:66`) and the tier now counts `fulfilled_via_wallet` alone, so a reversal leaves the counted set, which is the rule `product_trading` already applied to the selling rate |
| four rupee formatters that disagreed on the same amount, one rounding the paise away and one printing a single digit after the point, on the pages where a settled price is read back | one `money` in `web/lib/money.ts`, and the other three deleted rather than left beside it as alternatives | `money.test.ts`, which fails on a rounded amount, on one digit where there should be two, and on a lakh grouped the western way |
| a failed read answered the browser in the database's own words, naming the table and the constraint to anyone who could provoke one | every route hands the fault to `serverFault` in `web/lib/errors.ts`, which logs it whole and answers with one sentence that carries none of it | `errors.test.ts`, which fails if any of the fault text survives into the response, and if the log receives one field instead of the whole error |
| one retry key for every leg of a reversal that spans several funding payments, so the second leg is refused as a different request under a used key after the first has already sent money back | the key is derived per payment and amount where every caller passes through, `razorpay/refunds.go:131` | `refunds_test.go`, which fails if two legs of one reversal share a key, if retrying one leg does not reproduce its own, or if two separate reversals of equal amounts collide |
| the gateway refund rate counted refund objects against captured payments, so four one rupee test refunds read as a shop where nearly everything comes back, and that figure is what prices cover into a quote | refunded paise against captured paise, under its own divisor guard, `razorpay/sales.go:114` | `sales_test.go:67`, where one refund of 45,000 paise against 1,200,000 taken is three percent and not fifty |
| the gateway sales view had no caller | `cmd/market/main.go:68` hands the gateway to the trading provider, which reads it per five minute window at `trading/trading.go:112` and feeds the refund rate into the conditions the shop prices from | the row above it in the open table, which is the narrower defect that remains once the view is reached: it reads only the first page |
| an interrupted reversal was never resumed, so a gateway leg that failed or a process that stopped after the allowance was credited left the credit standing with no gateway evidence behind it, and the next refund returned before retrying it | the three things a resumed leg cannot reconstruct, the amount, the wording of the reason and the run, are written down beside the credit in the same transaction, migration `20260903000600`, and read back at `buyer/refund.go:185` instead of being rebuilt from the request in front of us | `refund_test.go`, where a second refund the wallet refuses still finishes the first attempt's reversal under that attempt's key, amount, reason and run rather than this one's, and `reversal_test.go`, where a leg the gateway already holds is counted and not sent again |
| a run was one shot, so a follow up message started over instead of continuing | what the shop showed is harvested out of the run and kept per chat under `agentmart:chat:{id}` for two hours, written before the failure check rather than after it, and the next message is asked against it at `shopgraph/builder.go:413` | `shopgraph_test.go:284` fails if the follow up does not lead the brief or if the earlier shortlist is missing from it; `cmd/user/conversation_test.go:205` drives a run that breaks after the shop has answered and fails if the shortlist was lost with it; `:160` drives a real graph against a remembered shortlist and then requires a settled purchase to clear it, so a bought shortlist is never refined |

Deferred with the reason stated, and on the merits rather than for time. The self
asserted account id stays, because the fix is either signing the id or issuing per
buyer tokens and neither is an hour's work; it is an authorization hole in the
personalisation path only, it cannot move money past the gate, and it is written
down here rather than left for someone to find. The bundled goods carry stays as
already sequenced in a later phase. The mandate is not deferred by
us at all: the route that charges one is withheld at the account level, which
section 5 now evidences, so that deferral belongs to the gateway. Resuming an
interrupted reversal is no longer deferred and is the last closed row above. The
shortcut of putting the current run id into the retry key was rejected on the way
there: a key the gateway has not seen makes a new refund rather than replaying the
one already made, so it would have given up a guard against a second refund to buy
replay. Writing the first attempt's inputs down buys the same replay and keeps the
guard, and the two guards that stood in front of it, the wallet ledger's own key
and the payment's remaining refundable amount, are untouched.

One change is not in that table because it was never on the defect list, and it
matters more than most of what was: a price could not settle below the list total,
which made the campaign tiers and the loyalty strategy decorative. Four layers
enforced that ordering: the gate ladder, the negotiation floor, the fulfillment
function and a check constraint on the revenue table. All four moved together. The
floor is now the list total less the discount a buyer is already entitled to, and
never below cost, so a discount is bounded by data rather than by a model's
judgement. The cost of that change is a row above: the buyer's gate no longer
double covers the cost floor, because the alternative was publishing cost to the
counterparty.

Also deferred, smaller: pagination past the first hundred gateway objects on the
sales view, the last write wins session store, the three environment variables that
promise configurability and are read by nothing, and the duplicate grant in the
initial schema. None is reachable in a short read, and all are recorded here rather
than left to be discovered.

Two defects were closed by the benchmark rather than by reading the code, which is
the argument for having built it. The buyer was escalating offers that sat inside
both the advisory band and the standing limit, because the escalation guidance
invited a person into every purchase: a trade off between price and bundle
describes every bundle. And an owner shown only stock that did not suit the brief
named no product, which the reader treated as unreadable output and turned into a
lost run. Both are fixed and both are covered by tests.

## 7. State and next upgrade

Built and merged: the conversational browse turn, the shopfront agent, the buyer
graph rewire, campaigns, both audit trails, the failure attribution layer, the
agent-run record, the approval handoff, a live twenty five situation audit that
runs the real agents with no mocks, one run correlated across the whole trail,
and a web surface where every operations figure is derived from rows by a tested
function rather than assembled inside a page.

Since then: settlement behind one swappable interface with a read-only gateway
sales view beside it; revenue reconciled against the money that actually moved,
with a disagreement shown as a figure on the page rather than a silence, which
required the fulfilment function itself to stamp the run on every row it writes;
a published comparison against a fixed price list, run live with no mocks, whose
methodology is stated above its own numbers; and a staged refusal that runs
against the real gate and is pinned as identical on a second run.

What the comparison is for. Its first two attempts each produced a number that
looked like a result and was not: the first summed revenue over scenarios the two
passes had not both completed, the second ran one pass into an exhausted provider
and the other into a recovered one. Both were reported as measurement defects
rather than published. The harness now pairs scenarios, reports the unpaired ones
with a reason, alternates the two shops per scenario so neither meets a different
provider, and splits revenue into settled and pending a person's approval so a
gate that did its job is not scored as a lost sale.

What it still cannot do is put a reliable number on the difference. Six scenarios
pair down to five as soon as the provider drops one, and two runs against nearly
the same tree disagreed by twenty two points on settled revenue purely because
they dropped different scenarios: one lost a sale, the other lost a decline. Both
runs agree on the shape, which is that the negotiating shop asks for more and the
extra ask crosses the buyer's rails often enough to turn a sale into an approval
request. The shape is what the comparison is published for. The percentage is not
a measurement, and the numbers page says so above its own table.

The remaining sequence, in order: move policy out of code into account rows, then
learn from outcomes. Conversation state was the first item on it and is the last
closed row in section 6.
