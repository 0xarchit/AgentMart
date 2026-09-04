# ۞ Security Policy

AgentMart moves money. It spends a prepaid allowance on a person's behalf, with no
human in the loop for an in-limit purchase, so a defect in the wrong place is a
financial defect rather than a cosmetic one. Reports are welcome and taken
seriously.

## § Supported versions

| Version | Supported |
| --- | --- |
| `v0.1.x` | ✔ |
| `main` | ✔ (unreleased) |
| anything older | ✘ |

There is one active line. Fixes land on `main` and go out in the next tag.

## ☍ Reporting a vulnerability

**Do not open a public issue for a security report.**

Use GitHub's private vulnerability reporting on this repository: **Security →
Advisories → Report a vulnerability**. That opens a private thread with the
maintainer and keeps the details out of the public timeline until there is a fix.

Please include:

- what an attacker gains, stated as an outcome rather than as a code smell
- the smallest reproduction you have, including the request shape if it is reachable
  over HTTP or through the database's REST gateway
- which surface it is on: the merchant service, the buyer service, the dashboard, or
  the database
- whether it needs a session token, a publishable key, a service key, or nothing

Expect an acknowledgement within a few days and an assessment with it. If a report
is valid you will be credited in the advisory unless you ask otherwise.

## ⌕ In scope

- Anything that moves money the person did not authorise, or moves more of it than
  they authorised
- Anything that widens a bound: the spend limit, the wallet balance, the cost floor,
  the funded discount entitlement, the standing ask
- Any way to reach another account's data, ledger, runs, or approvals
- Any way to make an agent's own output decide an amount, a basket, an identity, or a
  gate outcome, since every model-authored field is supposed to be clamped by
  deterministic code before it can
- Row level security gaps, privilege escalation, and column privileges reachable with
  only a session token and the project URL
- Secret exposure: keys in a response, a log line, a container image, or a bundle

## ✧ Out of scope

- The payment gateway runs in test mode. Findings that depend on real settlement
  behaviour are interesting but cannot be reproduced here.
- Rate limiting on the reasoning provider. It is a free pool, and it being slow or
  refusing is expected rather than a vulnerability.
- Self-inflicted configuration: a `.env` with a service key in a browser-reachable
  variable, or a deployment that publishes the merchant service to the internet when
  only the dashboard is meant to be exposed.
- The buyer's account identifier on the negotiation call is self asserted, and is
  documented as such in `docs/architecture.md`. It cannot move money past the gate.
  Reports that it can are very much in scope.

## ⊚ What we will not ask you to do

We will not ask you to test against anyone else's account, data, or wallet. If a
report needs a second account, create one; the sign-up flow is open.
