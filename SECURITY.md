# Security policy

## Reporting a vulnerability

Email **platform@clappform.com** with "security" in the subject. Please do
not open a public issue, a pull request or a discussion for a suspected
vulnerability: a public report tells everyone running the platform about the
problem before there is a fix.

Useful things to include, as far as you have them:

- what the issue is and which component it affects (API, cluster agent,
  webapp or a Helm chart);
- the version, release tag or commit you tested;
- how to reproduce it, and what an attacker gets out of it;
- any logs, requests or proof-of-concept you are willing to share.

We aim to acknowledge a report within five working days and to tell you
whether we can reproduce it. We will keep you updated while we work on a fix
and will credit you in the release notes if you want that.

Please give us a reasonable period to ship a fix before you publish. We are
happy to agree a disclosure date with you.

## Supported versions

| Version | Supported |
|---|---|
| 0.2.x | Yes |
| < 0.2 | No — development versions that predate the first public release |

Fixes land on `main` and go out in the next release of both charts.

## Scope

In scope: this repository — the Go API and cluster agent, the Nuxt webapp,
and the `saidkick-hub` and `saidkick-agent` Helm charts. Problems in how the
platform authenticates users, isolates organisations, brokers deployments to
spoke clusters, stores secrets or writes its audit log are all worth
reporting.

Out of scope: the applications that suppliers submit to a catalogue. Those
are third-party software and belong to the organisation that publishes them.
If you have found a vulnerability in a submitted application, report it to
that supplier. The evidence that applications must pass before approval is
described in [`docs/SUBMIT-YOUR-APP.md`](docs/SUBMIT-YOUR-APP.md).

Also out of scope: the dev and test fixtures in this repository. The
placeholder credentials in `API/.env.example`, `scripts/dev/`,
`scripts/kind-e2e/` and `charts/*/ci/` are deliberately fake and are not used
by any deployment.

## Running a deployment safely

[`API/RUNBOOK.md`](API/RUNBOOK.md) covers operating the platform, and
`charts/saidkick-hub/values-production.yaml` marks every value a production
install has to replace. In short: the API refuses to start in dev mode when
`APP_ENV=production`, the agent listener requires mutual TLS, and the audit
log is append-only and hash-chained. None of that helps if the hub is
installed with the example values, so replace them.
