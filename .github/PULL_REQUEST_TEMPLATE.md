# ❖ What changed

<!-- One or two sentences. What is different after this merges? -->

## ✦ What proves it

<!--
Name the test, the migration, or the run that would fail if this change were
wrong. "Manually checked" is not proof. A new test must have been mutation
verified: break the code it covers, watch it fail with a message that names the
real problem, restore, watch it pass.
-->

## ⊚ What you decided not to do

<!--
Scope you deliberately left out, and why. This is not a confession, it is the
most useful part of the review. If there is nothing, say so.
-->

## ☍ The gate

Every box below has to be ticked from a real run on your machine, not from
memory. `-short` is not optional: the long tests spend live paid provider quota.

- [ ] `gofmt -l` reports nothing on the files I touched
- [ ] `go build ./...`
- [ ] `go vet ./internal/... ./cmd/...` and `go vet ./Agents/`
- [ ] `go test -short -count=1 ./internal/... ./cmd/...`
- [ ] `cd web && npm run build` then `npx tsc --noEmit`
- [ ] `cd web && npx vitest run`

## ◈ If this touches money, the basket, an identity, or a gate decision

- [ ] No model-authored field can decide an amount, a basket, an identity, or a
      gate outcome without deterministic code clamping it first
- [ ] Every new bound has a test per named refusal reason
- [ ] Amounts stay `int64` paise end to end, with no float in the path

## ∿ If this touches the database

- [ ] A new migration file, correctly dated. No applied migration was edited
- [ ] `drop function if exists` on the old signature before `create or replace`
      with a new argument list
- [ ] `revoke` and `grant execute` re-issued, because a new function defaults to
      `execute` for `public`
- [ ] The migration is safe to apply to a database that already carries data

## § Notes for the reviewer

<!-- Anything that would take them longer than a minute to work out alone. -->
