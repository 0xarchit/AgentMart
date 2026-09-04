# ❖ Contributing

Thanks for looking. This is a small repository with an unusually strict middle: two
reasoning agents on the outside, and a money path in between that is not allowed to
be interesting. Most of what follows is about that middle.

## ⬢ The one rule that matters

**A model's output may never decide an amount, a basket, an identity, or a gate
outcome.** Every field an agent writes is clamped by deterministic code before it can
touch money. If you add a field to a structure a model fills in, ask what happens
when the model puts a hostile value there, and put the answer in code rather than in
a comment.

Fields that must never be model-writable are fenced out of the schema with
`json:"-"`. That tag is load bearing. Do not remove one without reading why it is
there.

## ⌕ Before you start

- Open an issue first for anything larger than a fix. It saves you writing something
  that is already sequenced differently.
- Read `docs/architecture.md`. It is the design contract, not an overview, and it
  states what is deliberately not built as clearly as what is.
- `docs/docs.md` is the implementation map: routes, data model, and where each thing
  lives.

## ∿ Setting up

Requires Go 1.26, Node 24, and a Postgres database with every migration in
`supabase/migrations/` applied in filename order.

```bash
cp .env.example .env    # database, gateway test keys, bot token, model access
go build ./...
cd web && npm ci
```

## ☍ The gate

Every commit has to pass all of this. Not "the part that changed" — all of it. A
commit that does not build and pass its own tests is not a commit here, because the
history is how the money path is audited.

```bash
gofmt -l .                                        # must print nothing
go build ./...
go vet ./internal/... ./cmd/...
go test -short -count=1 ./internal/... ./cmd/...
cd web && npm run build && npx tsc --noEmit && npx vitest run
```

`-short` is not optional. Without it the suite calls a live paid provider and takes
about thirty five minutes.

## ✦ Tests

New logic arrives with a test, and the test has to be one that can fail.

**Mutation-verify it.** Break the line you just wrote, confirm the new test fails
with a message that names the real problem, restore it, confirm green. A test that
passes against a deliberately broken implementation is worse than no test: it is a
claim nobody checked.

Failure messages are read by someone who does not have the code in front of them.
Prefer `quoted at %v, want the %v the run recorded` over `unexpected value`.

## § Commits

- One logical change per commit. A refactor and a fix are two commits.
- Branch per change. Never commit straight to `main`.
- Format: `<type>(<scope>): <imperative summary>`, subject **at most 50 characters**.
- Types in use: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `chore`, `ci`.
- No em dashes, no decorative symbols, no exclamation marks, no emoji in the subject.
- No tool attribution or co-author trailers.

```
fix(gate): refuse a quote older than the window
test(buyer): pin who a reversal spends against
ci: publish on tags, gate on code changes
```

Merge with `--no-ff`, or `--ff-only` for a genuinely self-contained single commit.

## ◈ Database changes

Schema is changed by adding a new migration, never by editing one that has been
applied. To redefine a function with a new parameter, `drop function if exists` the
old signature first: `create or replace` with a different argument list leaves both
callable, and a call matching both is rejected as ambiguous.

A migration that changes a money table or a function that writes one needs a comment
at the top saying what was wrong and why this is the fix. The migrations in this
repository read as a record of decisions, and that is on purpose.

Re-issue `revoke` and `grant execute` after dropping a function. A newly created
function gets default privileges, which means `execute` to `public`.

## ⊚ Pull requests

Fill in the template. The three things a reviewer needs are what changed, what proves
it, and what you decided not to do. The last one is not a formality: a pull request
that names its own trade-off gets read faster than one that hides it.

## ✧ Conduct

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Security
issues go through [SECURITY.md](SECURITY.md), never a public issue.
