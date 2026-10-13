# sighup

Rolls one or more Kubernetes Deployments whenever a container tag's digest moves in the registry.

CI ends at `docker push`. This watches the pushed tags and, when a digest changes, forces the
matching Deployments to roll so the new image is pulled. No CI credential ever touches the cluster:
sighup uses its own in-cluster ServiceAccount, scoped to `get,patch` on the **named Deployments** it
was pointed at.

## One sighup, a list of targets

A release names a *list* of targets, each one `repo:tag → namespace/deployment`. A single push can
fan out: every target watching that tag rolls. Adding a target is a line in the list, not a new
release with its own ServiceAccount, Service, webhook secret and Harbor webhook.

**Scope is the list.** The Role pins those Deployments by name, so the blast radius of a bug, a race,
or a stamped-on credential is exactly what the operator typed — never a wildcard, and never "every
Deployment in the namespace". A name you did not write is not in scope.

The ServiceAccount does not widen as the list grows; only the set of Roles bound to it does. One
identity, one Role (and RoleBinding) per namespace, each naming that namespace's Deployments.

## How it rolls

It patches a single pod-template annotation,
`sighup/applied`, whose value is the digest it is rolling to. One write does
both jobs: it records the intent, and because it is in the pod template it triggers the rollout.

The container image is left alone. It keeps the *moving* tag (e.g. `edge`), so sighup never
touches a field the Helm release owns. When Terraform renders the release with `image.tag = edge`
and `image.pullPolicy = Always`, the tag string is constant, so Helm and sighup never disagree
about anything that matters.

The check is idempotent: digest already recorded → do nothing. So a webhook storm, a poll, or a
restart all converge on the same state without looping.

## Modes

| Mode | Shape | Inbound | Webhook |
|---|---|---|---|
| `serve` | a Deployment | ClusterIP Service | yes, plus the poll loop |
| `once` | a CronJob | none | no |

`serve` listens for Harbor webhooks **and** polls on an interval. The poll is not optional: Harbor
retries a failed webhook only **three** times with a **3 second** timeout
(`webhook_job_max_retry: 3`, `webhook_job_http_client_timeout: 3`), so a missed delivery would
leave a Deployment stale forever. The webhook buys latency; the poll buys correctness.

`once` is the CronJob variant — poll only, by construction. It cannot receive a webhook, because a
CronJob has no socket. It checks every target and exits non-zero if any check failed.

## Configuration

All by environment variable. One registry, a list of targets.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `SIGHUP_REGISTRY` | yes | | registry host, e.g. `registry.example.com` |
| `SIGHUP_TARGETS` | yes | | JSON array of targets (below) |
| `SIGHUP_LISTEN` | no | `:8080` | `serve` listen address |
| `SIGHUP_POLL_INTERVAL` | no | `15m` | poll cadence |
| `SIGHUP_TIMEOUT` | no | `30s` | per-check timeout |
| `SIGHUP_WEBHOOK_SECRET` | no | *(unset = no auth)* | compared against the webhook header |
| `SIGHUP_WEBHOOK_HEADER` | no | `Authorization` | header carrying the secret |

`SIGHUP_TARGETS` is a JSON array; each entry names one Deployment and the moving tag that drives it:

```json
[
  {"repository": "library/someapp", "tag": "edge", "namespace": "app", "deployment": "someapp"},
  {"repository": "library/other",   "tag": "stable", "namespace": "app", "deployment": "otherapp"}
]
```

- `repository`, `tag`, `namespace`, `deployment` are all required.
- `annotation` is optional and defaults to `sighup/applied`.
- No two targets may name the same `namespace/deployment`: one writer per Deployment, enforced at
  startup rather than discovered as an annotation race.

Digest resolution is anonymous for public images (a public Harbor project, or public ghcr packages),
so no registry credentials at all. The webhook is **Harbor-specific**: ghcr sends no webhooks, so a
ghcr-only deployment runs on the poll loop (or the `once` CronJob) — the webhook endpoint simply
never fires.

## HTTP surface (`serve`)

| Path | Purpose |
|---|---|
| `POST /hook` | Harbor webhook; `202` accepted (some target matched), `200` ignored, `401` bad secret |
| `GET /healthz` | liveness |
| `GET /readyz` | readiness |

A Harbor webhook should be configured on the `library` project for `PUSH_ARTIFACT`, HTTP, payload
format **Default** (the CloudEvents format is not handled), pointing at the in-cluster Service, with
its auth header set to the same value as `SIGHUP_WEBHOOK_SECRET`. One webhook covers every target in
the release: a push is routed to the targets watching that `repo:tag` (possibly several), and the
rest are left alone. Because both Harbor and sighup are in the cluster, no gateway, certificate or
external DNS is involved.

## RBAC

One Role + RoleBinding **per target namespace**, each naming only that namespace's Deployments:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: sighup, namespace: <a target's namespace> }
rules:
  - apiGroups: [apps]
    resources: [deployments]
    resourceNames: [<the Deployments you named in that namespace>]
    verbs: [get, patch]
```

The ServiceAccount lives with the release; a cross-namespace subject is allowed, so a single identity
is bound into each target namespace. Widening the list widens the `resourceNames` — deliberately, and
never to a wildcard.

## Chart

`charts/sighup/` installs sighup in `serve` mode: the Deployment, a ClusterIP Service, the
ServiceAccount, and the RBAC — one Role per namespace that pins `target.deployment` **by name**. The
scope is the chart's, not something an operator can quietly widen.

```yaml
registry: registry.example.com
targets:
  - repository: library/someapp
    tag: edge
    deployment: someapp          # namespace defaults to the release namespace
  - repository: library/other
    tag: stable
    namespace: other             # only RBAC lands in this namespace
    deployment: otherapp
```

The webhook Secret is expected from outside (`webhook.secret.existingSecret`) so it never lands in
Helm values, nor in the release Secret where Helm stores them.
`webhook.secret.create` exists only for standalone use and says so.

```sh
make chart-lint    # helm lint --strict, with a throwaway target
make chart-guard   # asserts the chart still REFUSES to render without targets
```

There is no default target on purpose — a sighup must know nothing about any app — so `targets` must
be non-empty and every target's `repository`, `tag` and `deployment` are required. Note that
`helm lint` only logs missing required values as **warnings** (even under `--strict`) and still
exits 0, so `make chart-guard` exists to prove the guards are still there.

## Development

```sh
make test          # unit tests: a fake clientset + a stubbed digest source, no cluster needed
make run-once      # run `once` against your current kubeconfig (export SIGHUP_REGISTRY + SIGHUP_TARGETS)
make docker-build
```

## Gotchas

- **Helm strips the annotation.** A `tofu apply` that re-renders a Deployment drops the
  `sighup.../applied` annotation, which is itself a pod-template change → one extra rollout,
  after which the next check re-applies it. Harmless, but it means a Helm upgrade causes two rolls.
- **A bad push goes live.** Whatever CI published under the tracked tag is what runs, and there is
  no human gate unless you build one. Track a promoted tag (e.g. `stable`) instead of the tag CI
  pushes every commit to, and the gate becomes "who moves `stable`".
- **One writer per Deployment.** No two targets may name the same Deployment (sighup refuses to
  start if they do), and do not run a second sighup against one this one owns — two writers race the
  same annotation.
- **One registry per sighup.** `SIGHUP_REGISTRY` is shared by every target. A second registry is a
  second sighup.

