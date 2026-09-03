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
  |                 strategist decides, price guard bounds it at cost floor
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

### 7 to 9. Settlement

- **accept**: the buyer agent accepted, so the purchase goes through the gate.
  Wallet balance, spend limit, stock, and price freshness are checked, then one
  atomic ledger write moves the money and returns a receipt.
- **ask_human**: an approval row is written with a token, and the person gets
  the amount, the reasoning, the buttons, and the transcript. No money moves.
  The negotiation session stays open, because the deal is genuinely pending a
  person. On approval the purchase resumes through the same gate.
- **decline**: the session is closed with a reason. Nothing moves.

### What gets recorded

| Artifact | Written by | Contains |
| --- | --- | --- |
| session transcript | negotiation server | every turn from both agents, browse through settlement |
| `agent_run` | buyer | the decision, the reasoning, the amounts, and the conversation, before money moves |
| `offer_priced` | merchant | every quote and counter with its strategy and margin |
| gate decision | gate layer | approved or refused, with the reason |
| wallet ledger + receipt | wallet layer | the atomic money movement, returning the order id |
| merchant revenue | fulfilment function | base, final, and the generated uplift per order |
| exported `.txt` | user agent | the conversation as the person can read it |

Audit is fail closed on both sides. An offer that cannot be explained is not
sent. A purchase that cannot be recorded does not happen.

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
`internal/razorpay/sales.go` is a read-only view of gateway sales, structurally
read-only in that the file contains no write verb, and it has no caller outside
its own tests. It was described as shipped in this document until it was pointed
out that shipped has to mean a stranger can reach it. It is a package, not a
feature, until something calls it. `verifyCheckoutSignature` in
`web/lib/razorpay.ts` sat in this paragraph for the same reason and has since left
it: the browser callback now posts to `web/app/api/topups/confirm/route.ts`, which
verifies the signature and credits the wallet under the same idempotency key the
webhook uses, so the two paths race harmlessly and a lost webhook no longer
strands the money.

What was rejected, and why it matters. A payment link the person pays was the
obvious way to give the approval path a payment object, and it was turned down:
it puts a human in the per purchase loop, which is the one thing this design is
about not doing. The correct primitive is a mandate granted once, drawn against
by the agent. On the current test key a mandate can be granted but not drawn
against, so the honest position is that the adapter is ready and the capability
is pending, which is a one file swap rather than a redesign.

## 6. Known defects in the contract

| Defect | Where | Effect |
| --- | --- | --- |
| bundled goods not carried through the settling agent | `shopgraph/builder.go` negotiate branch | a negotiated bundle is still measured against the main product alone |
| a run is one shot | buyer graph | a follow up message starts over instead of continuing |
| the uplift bounds are chosen, not measured | `negotiation/conditions.go` | the amounts inside them are argued from observations, but the ceilings are judgement |
| no drawable mandate on the test key | `internal/buyer/settlement.go` | the approval path settles from the allowance, not from a payment object |
| the buyer's account id is self asserted | `negotiation/http.go`, one shared token for all callers | a caller can claim another account's loyalty tier and write trail rows against it |
| cost is enforced on the merchant side only | `internal/gate` has no cost knowledge | "never below cost" is proven where the price is set, not at both ends |
| the gateway sales view has no caller | `internal/razorpay/sales.go` | built and tested, and reachable by no running code |

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
| thirteen money path refusals returning with no trail row, where this list said three, including the amount integrity refusal that fires before the gate is consulted | one recorder per boundary that every refusal leaves through, `buyer/purchase.go:129` and `buyer/refund.go:67` | `purchase_trail_test.go` and `refund_test.go`, which fail both on a missing row and on a doubled one |
| the concession schedule was prose, not code | this round's concession floor is one of the bounds `clampToRails` chooses between, `marketgraph/graph.go:133` | `marketgraph_test.go:39`, which fails if the round's floor is only advisory |
| the checkout signature verifier had no caller | `web/app/api/topups/confirm/route.ts` credits the wallet from the browser callback, under the idempotency key the webhook already uses | `razorpay.test.ts` covers the four facts that decide the credit, including a captured payment replayed by an account it was not opened for |
| a tool returned its own argument | `get_current_terms` is removed rather than implemented, since a tool that hands back its own argument is worse than a missing one | no toolset registers the name and no code refers to it |
| a ten minute window on account and product refused purchases that were not duplicates: a second unit, a gift, a fresh attempt after an unrelated failure | the idempotency key is left to be the duplicate check it already is, migration `20260903000400` | every purchase key is derived from the message or the negotiation session that asked for it, so a retry carries the same key and a genuinely new purchase carries a new one; runaway repetition is bounded by the balance, the spend limit and the approval rail instead of by a clock |
| a loyalty tier was farmable by buying and refunding in a loop, and the tier is not decoration: it is the entitlement that sets the floor a price may settle to | the tier counts only the orders whose money stayed, migration `20260903000500` | structure rather than a test, since nothing here runs SQL: `refund_wallet_order` moves a reversed order to `refunded_via_wallet` (`20260823000500_refund_contract.sql:66`) and the tier now counts `fulfilled_via_wallet` alone, so a reversal leaves the counted set, which is the rule `product_trading` already applied to the selling rate |

Deferred with the reason stated, and on the merits rather than for time. The self
asserted account id stays, because the fix is either signing the id or issuing per
buyer tokens and neither is an hour's work; it is an authorization hole in the
personalisation path only, it cannot move money past the gate, and it is written
down here rather than left for someone to find. The bundled goods carry and the one
shot run stay as already sequenced in later phases, as does the mandate.

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

Also deferred, smaller: pagination past the first hundred gateway objects, the
last write wins session store, the three environment variables that promise
configurability and are read by nothing, and the duplicate grant in the initial
schema. None is reachable in a short read, and all are recorded here rather than
left to be discovered.

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

The remaining sequence, in order: hold conversation state across messages, then
move policy out of code into account rows, then learn from outcomes.
