# AgentMart

A shopping agent that buys from a merchant's own selling agent, over chat, with
every rupee bounded by code: a live audit walks eleven shopping situations end to
end against the real agents with no mocks, and not one run in any of them has
priced below the merchant's cost floor.

Two agents negotiate. One represents a person and spends from a funded allowance
with a standing limit. The other represents the shop and prices from a real
catalog. Neither can move money: charge creation lives in Go, behind a gate that
re-derives every amount from the catalog and refuses in a named way.

## The brief, and where each requirement is proven

Track 01 asks for an agent that grows a merchant's revenue or makes a merchant
transactable by an AI buyer, with every money action explainable, bounded and
gated, plus the audit trail and one failure handled gracefully.

| Requirement | How it is met | Where to look |
| --- | --- | --- |
| Every money action explainable | Every refusal carries an ordered reason, every offer records the rule that priced it, and one run identifier ties the conversation to the money rows it caused | `internal/gate/gate.go`, `internal/marketaudit/`, `supabase/migrations/20260830000100_run_correlation.sql` |
| Bounded | Never below cost, never above the standing ask, never above the balance or the spend limit, one purchase per idempotency key, nothing crosses unrecorded | `internal/gate/gate.go`, `internal/marketgraph/graph.go`, `supabase/migrations/20260825000100_fulfillment_idempotency_lock.sql` |
| Gated | An amount over the limit is refused and handed to the person with a token; the gate fails closed if it cannot record its own decision | `internal/buyer/purchase.go`, `internal/gate/gate.go` |
| No model can spend | Charge creating tools are kept out of every tool set, and that refusal is a test rather than a promise | `TestNoMoneyMovingToolReachesAReasoningLayer` |
| Show the audit trail | One conversation read back as words on the left and money on the right | `/dashboard/runs`, view `run_timeline` |
| One failure handled gracefully | A quote above the limit is refused, nothing is spent while the answer is outstanding, and approval settles the exact amount that was quoted. Pinned as identical on a second run | `internal/buyer/staged_failure_test.go` |
| Payment gateway, test mode | A real order and a real captured payment fund the allowance, verified by signature and credited once per payment id; every agent purchase writes a gateway order object | `web/app/api/razorpay/`, `internal/razorpay/orders.go` |

## Run it

Requires Go 1.26, Node 24, and a Postgres database with the migrations in
`supabase/migrations/` applied in filename order.

```bash
cp .env.example .env      # fill in database, gateway test keys, bot token, model access
go build ./...
go run ./cmd/market       # merchant: catalog, negotiation, agent surface, :8081
go run ./cmd/user         # buyer: chat bot and the shopping graph, :8082
cd web && npm ci && npm run dev   # storefront, dashboard, run view, :3000
```

Then message the bot in plain words: `buy me a trimmer under 2500`.

Tests: `go test ./internal/... ./cmd/...` for the money paths and the graphs,
`cd web && npx vitest run` for the figures on the dashboard. Every displayed
number is produced by a tested function rather than assembled inside a page.

## How it fits together

```text
person, in chat
  |
  v
buyer agent ......... reads a brief, chooses, judges the offer, negotiates
  |                   spends from an allowance with a standing limit
  |  asks for a quote
  v
merchant agent ...... greets, pitches real stock, prices inside its rails
  |                   may attach a partner product, may concede, never below cost
  |  returns a quoted amount
  v
gate layer .......... re-derives the amount from the catalog and decides:
  |                   approved, or refused with a named reason, or a person's call
  |                   records the decision before returning, or refuses to return
  v
money ............... one atomic debit against the allowance, one gateway order
                      object, one audit row, all carrying the same run identifier
```

Both agents reason live. There is no scripted decision path inside either graph:
when the provider fails, the failure surfaces and names the layer it came from
rather than degrading into branches that look like judgement.

## What is real, and what is not

Worth stating plainly, because the difference is the whole credibility of the
rest.

Real: the allowance is funded by a genuine captured test-mode payment, verified by
signature and credited exactly once per payment id. Every agent purchase creates a
gateway order object. Every bound in the gate is enforced in code and covered by a
test per reason. The situation audit and the paired benchmark run the real agents
against a real provider, with nothing replayed or mocked, and both have caught
real defects in our own agents that reading the code had not. Twenty five
situations are catalogued and eleven of them run end to end today; the rest need
capabilities this system does not have yet, which is recorded rather than hidden.

Not real yet: the settling step itself moves money inside our own ledger rather
than drawing on the gateway. The design for that is a mandate authorised once and
drawn per purchase with no person in the loop, and it is not available on a test
key today: an authorisation can be granted and not charged against. Settlement is
therefore one interface behind the gate, so enabling it is a one file change and
not one bound moves.

We deliberately rejected the easier route of a payment link per purchase. It would
have produced a real captured payment for every sale, and it puts a person in
every transaction, which contradicts the one thing this system is for.

## Known limitations

- A price may settle below list only as far as the buyer's funded loyalty
  entitlement, and never below cost. With no campaign the floor is the list total,
  which is what every anonymous caller gets.
- Cost is enforced on the merchant side only. The buyer's gate protects the
  person's money and has no knowledge of what anything costs, so "never below
  cost" is proven where the price is set rather than at both ends. The alternative
  was publishing cost to the counterparty, which we rejected. This is a deliberate
  reduction in defence depth on that one claim.
- A run is one shot for pricing, but no longer for conversation. A follow-up such
  as "the second one" or "cheaper" continues against the shortlist the shop last
  showed, and a message sent while a decision is outstanding answers that decision
  instead of starting again. What is carried forward is the conversation only:
  every amount is re-derived and every bound re-read on each run.
- The opening quote's bounds are chosen, even though the amounts inside them are
  not. What the shop may add for cover, handling and scarcity is argued from the
  selling rate, stock cover and the gateway's refund rate, and nothing is charged
  for unless the fact behind it was read. The ceilings on each of those, and the
  twelve percent ceiling over list, are still a judgement call rather than a
  measurement.
- The buyer's account identifier on the negotiation call is self asserted. It
  cannot move money past the gate, which re-derives every amount, but it can
  claim another account's loyalty tier.
- Reasoning runs against a free model pool that is often rate limited. Latency is
  traded for reliability on purpose: each model is retried before the next is
  tried.

The design contract behind these choices is in `docs/architecture.md`, and the
implementation map, routes, data model and verification steps are in
`docs/docs.md`. The measured comparison against a fixed price list, with its
methodology stated above its own numbers, is in `docs/benchmark.md`.
