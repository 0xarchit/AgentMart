# Technical reference

Implementation detail for someone reading or running the system. The design
reasoning behind these choices is in `architecture.md`; this file is the map.

## Processes

Three processes, run independently.

| Process | Command | Default port | Serves |
| --- | --- | --- | --- |
| merchant | `go run ./cmd/market` | 8081 | catalog reads, offer pricing, the negotiation endpoint, the agent surface, `/health` |
| buyer | `go run ./cmd/user` | 8082 | the chat bot, the shopping graph, purchases, refunds, `/diag` |
| web | `npm run dev` in `web/` | 3000 | storefront, account dashboard, run view, operations view, gateway webhook |

The merchant's endpoints sit behind a shared bearer token, except `/health`. The
buyer reaches it as a client; nothing about the split requires them to be on one
machine.

## Merchant surface

| Route | Purpose |
| --- | --- |
| `POST /negotiation` | the whole conversation: browse, propose, counter, accept, decline |
| `/mcp` | catalog tools for a reasoning layer: search, get one product, check stock, get offers |
| `/a2a`, `/a2a/` | the agent surface, including the card at `/a2a/.well-known/agent-card.json` |
| `/health` | unauthenticated liveness |

## Web surface

| Path | Purpose |
| --- | --- |
| `/` | storefront, reads the public catalog |
| `/login` | customer sign in |
| `/admin/login` | operator sign in, a separate door that grants nothing by itself |
| `/dashboard` | allowance, spend limit, orders, wallet movements, recent runs, chat linking |
| `/dashboard/runs` | one run read back as conversation on the left and money on the right |
| `/admin` | operations figures, reconciled against the ledger. Open only to an account whose type is admin |
| `/api/topups/orders` | creates a gateway order for funding the allowance |
| `/api/razorpay/webhook` | verifies the signature and credits the allowance once per payment id |
| `/api/account`, `/api/account/spend-limit` | read the account, change the standing limit |
| `/api/link-tokens` | mints a token that links a chat account |

## Module map

Buyer side:

| Package | Responsibility |
| --- | --- |
| `internal/shopgraph` | the buyer as a graph: choose, quote, judge, negotiate, settle |
| `internal/buyer` | purchases, approvals, refunds, settlement, the trail writer |
| `internal/gate` | the only thing that authorises a spend |
| `internal/negotiationclient` | talks to the merchant's negotiation endpoint |
| `internal/marketclient`, `internal/remotemerchant` | catalog tool client, remote merchant adapter |
| `internal/telegram`, `internal/linking` | chat transport and account linking |

Merchant side:

| Package | Responsibility |
| --- | --- |
| `internal/marketgraph` | the shop as a graph: greet and pitch, price, choose a strategy, guard the price |
| `internal/negotiation` | session state, the offer policy, the wire shape |
| `internal/markettools` | the catalog tool server |
| `internal/merchantagent`, `internal/buyeragent` | agent cards and skills on both sides |
| `internal/campaigns` | loyalty tier lookup for a known buyer |
| `internal/trading` | the shop's own selling rate and the gateway's refund rate |
| `internal/marketaudit` | records every priced offer, and fails closed |

Shared:

| Package | Responsibility |
| --- | --- |
| `internal/llmchat` | one reasoning wire: schema-forced answers, retries, model chain |
| `internal/catalog` | product reads and the search contract |
| `internal/razorpay` | gateway order creation, and a read-only sales view |
| `internal/wallet` | allowance movements through database functions |
| `internal/runid` | the run identifier, carried on the context |
| `internal/failure` | turns an outage into a sentence naming the layer |
| `internal/health` | per-layer probes behind `/diag` |
| `internal/supabase` | the database REST client |

## The gate, in order

`internal/gate/gate.go` answers every spend request with one of these, checked in
this order. The first that matches is the answer.

```text
missing_identity
invalid_quantity
invalid_amount
amount_overflow
amount_mismatch                 the base amount does not match the live catalog row
insufficient_stock
human_approval_required         no limit set, or the total is above it
insufficient_wallet_balance
stale_price
approved
```

The decision is recorded before it is returned. If the recording fails, the gate
refuses rather than approving something it could not write down.

A settled amount may sit below the list total, which is what a funded loyalty
discount produces. That bound belongs to the merchant, not to this gate: the
floor is the list total less whatever discount the buyer is entitled to, and
never below cost. See the known limitation about where cost is enforced.

## Money flow

1. A person funds the allowance from the dashboard. That is a real gateway order
   and a real captured payment, verified by signature, credited exactly once
   keyed on the payment id.
2. The buyer agent asks the merchant for a quote. The merchant prices inside its
   rails and records the offer before returning it.
3. The buyer judges the quote against the person's money facts.
4. The gate re-derives the amount from the catalog and decides. Above the limit
   means a person is asked, and nothing is spent while that answer is
   outstanding.
5. A settlement creates a gateway order object and moves the allowance in one
   atomic database call, under an advisory lock keyed on the idempotency key.
6. A cancellation inside the refund window credits the allowance back, restocks
   the product, and reverses the funding payments at the gateway. A gateway leg
   that does not complete leaves what it owes recorded, and the next attempt
   finishes that one instead of sending a second refund.

Every row written in steps 2 through 6 carries the same run identifier, which is
stamped once per inbound message and threaded through both processes.

## Data model

Tables: `accounts`, `products`, `orders`, `wallet_ledger`, `merchant_revenue`,
`audit_log`, `campaigns`, `human_approval_requests`, `link_tokens`,
`telegram_links`, `reversal_attempts`.

`accounts.account_type` is either customer or admin and decides who may read the
operator view. A browser session cannot change it: the column privilege is revoked
from signed in users and a trigger refuses the change regardless, so the one
column that decides what an account can see is the one it cannot set for itself.

`reversal_attempts` holds one row per refunded order while the gateway side of it
is still owed, carrying the amount, the reason, the idempotency key and the run the
credit was made under. Those are the inputs the gateway hashes into a refund
request, so a reversal interrupted after the credit is resumed from this row rather
than rebuilt from whatever the next message says, which is what makes the resumed
leg a replay instead of a second refund.

Views: `run_summary`, `run_timeline` and `product_trading`, all with
`security_invoker` on, so a reader sees only their own rows. `product_trading`
carries the selling rate and stock cover an opening quote is priced from, with a
cover of minus one meaning no sales were observed rather than none exist.

Database functions, which is where money actually moves:

| Function | Purpose |
| --- | --- |
| `credit_wallet_topup` | credits a verified captured payment, once per payment id |
| `fulfill_wallet_order` | the atomic purchase: debit, order, revenue, trail |
| `refund_wallet_order` | the reversal, with restock, recording what the gateway still owes |
| `create_human_approval`, `resolve_human_approval` | the handover and its answer |
| `redeem_telegram_link` | links a chat account to a database account |
| `campaign_for_account` | the loyalty tier for a known buyer |

Row level security is on every table with own-row policies. The money functions
are revoked from anonymous and signed-in roles and granted to the service role
only, so the browser cannot reach them even with a valid session.

Migrations live in `supabase/migrations/`, twenty nine of them, applied in
filename order.

## Configuration

`.env.example` lists every variable. The ones without which nothing runs:

| Variable | Used for |
| --- | --- |
| `SUPABASE_URL`, `SUPABASE_SECRET_KEY`, `SUPABASE_PUBLISHABLE_KEY` | database access, server and browser |
| `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`, `RAZORPAY_WEBHOOK_SECRET` | gateway orders and webhook verification |
| `TELEGRAM_BOT_TOKEN` | the chat transport |
| `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `ADK_MODEL_NAME` | reasoning access and the model chain |
| `MARKET_SHARED_TOKEN` | the token the buyer presents to the merchant |
| `USER_MARKET_MCP_ENDPOINT`, `USER_MARKET_A2A_ENDPOINT` | where the buyer finds the merchant |
| `UPSTASH_REDIS_REST_URL`, `UPSTASH_REDIS_REST_TOKEN` | the Upstash Redis REST endpoint and token; the merchant exits at startup without them, the buyer starts and silently loses per chat conversation memory, so a follow up referring back to what was already shown stops resolving |
| `DEFAULT_SPEND_LIMIT_PAISE` | the standing limit a new account starts with |

`ADK_MODEL_NAME` is a comma separated chain. Each model is retried five times,
three seconds apart, before the next is tried, because a free pool flaps per call
and reliability is worth the latency. A spent allowance is not retried.

Three variables are currently read by no code and are documented as such rather
than removed silently: `REFUND_WINDOW_MINUTES`, `WALLET_TOPUP_MAX_PAISE` and
`RAZORPAY_ACCOUNT_NUMBER`.

## Languages, and why each one is here

| Language | Where | Why this one |
| --- | --- | --- |
| Go | both agent processes, the gate, every money path | The money rails have to be reachable by a reader and hard to make racy. Static types over amounts in paise, one binary per process with no runtime to install, and `context` cancellation that carries the run identifier through every call without a parameter for it. Integer arithmetic throughout: no amount is ever a float. |
| TypeScript | the web surface | Every figure shown to a person is derived by a typed function that is unit tested, rather than assembled inside a page. Types on the row shapes are what make that safe to change. |
| SQL | the money movements themselves | A debit, an order, a revenue row and a trail row must either all happen or none. That is a transaction, so it belongs in the database as a function under an advisory lock, not in application code holding four round trips open. Row level security then bounds what any session can read at all. |

Particularly, the split is not stylistic. Money moves in SQL because atomicity is
a database property; money is authorised in Go because that is where the reasoning
and the bounds meet; money is displayed in TypeScript because that is where a
person reads it.

## Frameworks and major libraries

Six direct Go dependencies, five runtime web dependencies. All pinned to exact
versions in both manifests.

| Dependency | Version | Used in | For what |
| --- | --- | --- | --- |
| agent workflow framework, `google.golang.org/adk/v2` | 2.2.0 | `internal/shopgraph`, `internal/marketgraph`, `internal/llmchat` | Both agents are graphs, not loops: typed nodes, an edge builder with routes and fan-in, and an event stream per run. The graph shape is what makes each step separately recordable, which is what the trail needs. |
| model content types, `google.golang.org/genai` | 1.66.0 | `internal/llmchat`, both graphs | The content and schema types the framework speaks. Carried directly because the reasoning wire builds its own requests. |
| tool server SDK, `github.com/modelcontextprotocol/go-sdk` | 1.7.0 | `internal/markettools`, `internal/marketclient` | The catalog is exposed as tools a reasoning layer can call: search, one product, stock, offers. Same library serves the merchant side and reads it from the buyer side. |
| agent surface SDK, `github.com/a2aproject/a2a-go/v2` | 2.5.0 | `internal/merchantagent`, `internal/buyeragent`, `internal/negotiationclient` | Both sides publish a card describing their skills, and the negotiation travels as agent to agent messages rather than a private REST shape. This is what makes the merchant transactable by a buyer nobody here wrote. |
| schema generation, `github.com/google/jsonschema-go` | 0.4.3 | `internal/llmchat/schema.go` | Each node's answer schema is derived from its own Go result type, so the shape a model is asked for cannot drift from the struct that receives it. |
| identifiers, `github.com/google/uuid` | 1.6.0 | `internal/runid` | One identifier per inbound message, stamped once and carried on the context. |
| Next.js | 16.3.2 | `web/` | Server components read the database directly with the service role kept server side, so no figure passes through a browser-trusted path on its way to the page. |
| React | 19.2.8 | `web/` | What Next.js renders. |
| database client, `@supabase/supabase-js` and `@supabase/ssr` | 2.112.3, 0.12.4 | `web/lib`, every page | Typed row reads and cookie-based sessions, so row level security applies to the signed-in user rather than to a shared connection. |
| Tailwind CSS | 3.4.17 | `web/` | A five colour palette and a shared card pattern, applied without a component library. |
| TypeScript | 6.0.3 | `web/` | Checked with no emit as a gate, separately from the build. |
| Vitest | 4.1.11 | `web/lib` | Unit tests over the figure functions. Fifty four tests across eight files. |

Go's standard library covers the rest, deliberately: `net/http` for every server
and client, `encoding/json` for every wire shape, `log/slog` for structured logs,
`sync` for the few places that need it, and `testing` with `net/http/httptest` in
fifty three test files, twenty nine of which stand a real server up rather than
mocking one. There is no web framework, no router, no ORM, no assertion
library and no mocking library anywhere in the Go tree. Webhook signature
verification is the one piece of cryptography here and it lives in the web app,
using the runtime's own `crypto`, because that is where the gateway delivers.

## What was deliberately not added

The absences are choices, and each one was argued rather than defaulted.

| Not used | Instead | Why |
| --- | --- | --- |
| a component library | a shared `Card` and `Stat` pattern in one file | The pages are plain by intent and legible on camera. A library would cost days of integration and change nothing a reader values. |
| a charting library | one hand written bar panel, about fifteen lines | It is the only chart in the product. A dependency for one chart is a dependency that can break during a demo. |
| an ORM or query builder | the database REST client and SQL functions | The money movements are already SQL that has to be read carefully. An abstraction over them would hide the exact thing that needs auditing. |
| a mocking framework | small hand written fakes next to each test | The fakes record what they were asked and replay only that. A test that reads as an English sentence is worth more than one that configures a mock. |
| an HTTP client library | `net/http` with explicit timeouts | Every outbound call has a deadline chosen for its layer. A convenience wrapper tends to hide that. |
| a logging framework | the standard structured logger | Layer attribution is done by `internal/failure`, which turns a fault into a sentence. That is the part that mattered, not the log format. |

## Verifying it

```bash
gofmt -l internal/ cmd/
go build ./...
go vet ./...
go test -short -count=1 ./internal/... ./cmd/...     # 28 packages
cd web && npx tsc --noEmit && npx vitest run          # 54 tests, 8 files
```

The Go test paths are scoped on purpose rather than written as `./...`. The
repository also carries live harnesses that drive the real agents against a real
provider, and those take tens of minutes and need provider credentials, so they
are run deliberately and never as part of the ordinary suite.

Notable tests, because they encode contracts rather than behaviour:

| Test | Contract |
| --- | --- |
| `TestNoMoneyMovingToolReachesAReasoningLayer` | no charge creating tool is in any tool set |
| `TestAnAskAboveTheLimitIsRefusedThenSettledOnlyAfterApproval` | the staged refusal, against the real gate |
| `TestTheStagedSequenceRunsTheSameWayTwice` | that sequence is reproducible as text |
| `TestAFairBundleIsNotJudgedAsMarkupOnTheMainProduct` | attached goods count as list value |
| `TestAFlappingModelIsAskedAgainRatherThanAbandoned` | a chain does not shrink the retry budget |
| `clampToRails` tests | no strategy can price below the cost floor |

## Failure behaviour

There is no scripted decision path inside either graph. When a provider fails,
the failure surfaces with the layer named and a check to make, rather than
degrading into branches that resemble judgement. `/diag` on the buyer probes each
layer independently, so an outage is attributed rather than guessed at.
