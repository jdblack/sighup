# sighup

Rolls a Kubernetes Deployment whenever a container tag's digest moves in the registry.

CI ends at `docker push`. This watches the pushed tag and, when its digest changes, forces the
Deployment to roll so the new image is pulled. No CI credential ever touches the cluster: sighup
uses its own in-cluster ServiceAccount, scoped to `get,patch` on **one named Deployment**.

## One sighup per target

A release names **one** image tag and **one** Deployment. That is not a limitation to engineer
around — it is where the safety comes from: the Role names that single Deployment, so the credential
physically cannot reach anything else.

A herd is therefore N releases of this chart, each with its own `target` and its own two-object
Role. In Terraform that is a `for_each` over a map of targets, not a smarter binary.

## How it rolls

It patches a single pod-template annotation,
`sighup.vn.linuxguru.net/applied`, whose value is the digest it is rolling to. One write does
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
leave the Deployment stale forever. The webhook buys latency; the poll buys correctness.

`once` is the CronJob variant — poll only, by construction. It cannot receive a webhook, because a
CronJob has no socket.

## Configuration

All by environment variable.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `SIGHUP_REGISTRY` | yes | | e.g. `harbor.vn.linuxguru.net` |
| `SIGHUP_REPOSITORY` | yes | | e.g. `library/someapp` |
| `SIGHUP_TAG` | yes | | the moving tag to track, e.g. `edge` |
| `SIGHUP_NAMESPACE` | yes | | namespace of the Deployment |
| `SIGHUP_DEPLOYMENT` | yes | | name of the Deployment |
| `SIGHUP_ANNOTATION` | no | `sighup.vn.linuxguru.net/applied` | the annotation it owns |
| `SIGHUP_LISTEN` | no | `:8080` | `serve` listen address |
| `SIGHUP_POLL_INTERVAL` | no | `15m` | poll cadence |
| `SIGHUP_TIMEOUT` | no | `30s` | per-check timeout |
| `SIGHUP_WEBHOOK_SECRET` | no | *(unset = no auth)* | compared against the webhook header |
| `SIGHUP_WEBHOOK_HEADER` | no | `Authorization` | header carrying the secret |

The `library` project in Harbor is public, so digest resolution is anonymous — no registry
credentials at all.

## HTTP surface (`serve`)

| Path | Purpose |
|---|---|
| `POST /hook` | Harbor webhook; `202` accepted, `200` ignored, `401` bad secret |
| `GET /healthz` | liveness |
| `GET /readyz` | readiness |

A Harbor webhook should be configured on the `library` project for `PUSH_ARTIFACT`, HTTP, payload
format **Default** (the CloudEvents format is not handled), pointing at the in-cluster Service, with
its auth header set to the same value as `SIGHUP_WEBHOOK_SECRET`. Because both Harbor and sighup
are in the cluster, no gateway, certificate or external DNS is involved.

## RBAC

Two objects, nothing else — one verb, on one named object:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: sighup, namespace: <the target's namespace> }
rules:
  - apiGroups: [apps]
    resources: [deployments]
    resourceNames: [<the Deployment you name>]
    verbs: [get, patch]
```

## Chart

`charts/sighup/` installs sighup in `serve` mode: the Deployment, a ClusterIP Service,
the ServiceAccount, and the RBAC — one Role that pins `target.deployment` **by name**. The privilege
is the chart's, not something an operator can quietly widen.

The webhook Secret is expected from outside (`webhook.secret.existingSecret`) so it never lands in
Helm values, nor in the release Secret where Helm stores them.
`webhook.secret.create` exists only for standalone use and says so.

```sh
make chart-lint    # helm lint --strict, with a throwaway target
make chart-guard   # asserts the chart still REFUSES to render without a target
```

There is no default target on purpose — a sighup must know nothing about any app — so
`target.repository`, `target.tag` and `target.deployment` are all `required`. Note that
`helm lint` only logs missing required values as **warnings** (even under `--strict`) and still
exits 0, so `make chart-guard` exists to prove the guards are still there.

## Development

```sh
make test          # unit tests: a fake clientset + a stubbed digest source, no cluster needed
make run-once      # run `once` against your current kubeconfig
make docker-build
```

## Gotchas

- **Helm strips the annotation.** A `tofu apply` that re-renders the Deployment drops the
  `sighup.../applied` annotation, which is itself a pod-template change → one extra rollout,
  after which the next check re-applies it. Harmless, but it means a Helm upgrade causes two rolls.
- **A bad push goes live.** Whatever CI published under the tracked tag is what runs, and there is
  no human gate unless you build one. Track a promoted tag (e.g. `stable`) instead of the tag CI
  pushes every commit to, and the gate becomes "who moves `stable`".
- **One writer per Deployment.** Run one sighup per Deployment, or two will race the same
  annotation.
