# sighup

Sighup watches Docker and Harbor registries (any Docker Registry v2 / OCI registry will do) for
changes to moving tags like "latest", "dev" or "edge". When the digest behind a watched tag changes,
Sighup triggers a rolling restart of the matching Deployments, the same effect as
`kubectl rollout restart`.

There are a couple of requirements for this to work. Firstly, the Deployment **must** set
`image.pullPolicy = "Always"`, so the replacement pods actually pull the new image. Secondly, Sighup
depends on a small ServiceAccount that is allowed to roll Deployments. Its permissions are quite
limited: `get` and `patch` on the Deployments you want to control, and nothing more.

The usual story looks like this. Your CI finishes at `docker push` and stops
there. It never touches the cluster, and no CI credential ever has to live
inside it. That is where Sighup comes in. It runs inside the cluster, watches
the tags you care about, and the moment a digest changes it forces the matching
Deployments to roll. There is no pipeline secret to hand it, no kubeconfig
stashed in a CI variable, and nothing extra to rotate.

## One Sighup for all the things

You need one Sighup per registry you want to track. Each one, though, can
manage as many Deployments as you like, as long as they pull images from that
registry. You give a Sighup a list of *targets* in JSON format, and each
`repo:tag` is mapped to a `namespace/deployment` 1:1.  It is perfectly fine to
list the same `repo:tag` multiple times, each pointing to a different
`namespace/deployment`. 

Sighup relies upon a Service account that grants `get` and `patch` to deployments,
and a Role/RoleBinding pair for each namespace that it shold manage. The helm chart
will happily create all of this for you if you provide the helm release with the list
of `targets` to track.

If you wish to deploy Sighup manually, then you'll need to create the ServiceAccount and
Role/Rolebinding pairs yoruself. See [RBAC](#rbac) below for the exact process.

## How a deployment roll actually works

Sighup never edits the container image. When a watched tag's digest moves, it writes a single
annotation named `sighup/applied` onto the Deployment's pod template, set to the new digest. Because
the annotation lives in the pod template, that one write changes the template and triggers a rolling
restart. The image reference is left alone, still holding the moving tag such as "edge", so Sighup
never touches a field the Helm release owns.

It is very important that the Deployment sets `image.pullPolicy = "Always"`. With a pull policy of
`Never` or `IfNotPresent`, the nodes will happily serve the old cached image, and the rollout will
change nothing.

The check is idempotent as well. If the digest has already been recorded, Sighup does nothing, which
means a storm of webhooks, a scheduled poll, or a restart all settle on the same state instead of
looping.

## Tracking Modes

Sighup can learn that a tag moved in three ways. Two of them are the same binary mode, `serve`; the
third is its own mode, `cronjob`.

| Method | Mode | Runs as | What triggers a check |
|---|---|---|---|
| **webhook** | `serve` | a Deployment | your registry POSTs to `/hook`, with the poll loop as a safety net |
| **polling** | `serve` | a Deployment | the in-process poll loop, every `SIGHUP_POLL_INTERVAL` |
| **cronjob** | `cronjob` | a CronJob | Kubernetes runs Sighup on your schedule |

`serve` is the resident mode. It runs as a Deployment and stays up: it checks every target at
startup, then serves the webhook endpoint (and the probes) while polling on an interval. The poll
loop is not optional, and here is why. Harbor retries a failed webhook only **three** times, with a
**3 second** timeout (`webhook_job_max_retry: 3`, `webhook_job_http_client_timeout: 3`), so a single
missed delivery would leave a Deployment stale forever. The webhook buys you latency, but the poll
buys you correctness. There is no webhook-without-poll.

`cronjob` is the scheduled mode. It checks every target and exits, starting neither a listener nor a
loop, so the schedule lives in Kubernetes rather than in the process. Every run is independent, and a
run exits non-zero if any check failed, which Kubernetes surfaces as a failed Job.

In practice:

- **webhook** is `serve` with a Service in front of it: the lowest-latency path, and the reason
  `serve` exists. It needs a registry that can send webhooks (Harbor can; ghcr cannot).
- **polling** is `serve` with nothing in front of it (`service.enabled=false`), or simply no webhook
  pointed at it. The container still polls on its interval, and since nothing can reach the endpoint,
  the poll loop is the only trigger. This is the pick for a registry that cannot send webhooks, or
  when you would rather expose nothing.
- **cronjob** is the `cronjob` mode under a Kubernetes CronJob: no resident pod, no Service, no
  webhook. The interval is whatever `schedule` you give the CronJob. The chart can create this for
  you with `cronjob.enabled=true` (see [Chart](#chart)).

Configuring the webhook itself is covered under [HTTP surface](#http-surface-serve).

## Configuration

Everything is configured through environment variables. A single Sighup watches one registry, and
carries a list of targets.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `SIGHUP_REGISTRY` | yes | | registry host, e.g. `registry.example.com` |
| `SIGHUP_TARGETS` | yes | | JSON array of targets (below) |
| `SIGHUP_LISTEN` | no | `:8080` | `serve` listen address |
| `SIGHUP_POLL_INTERVAL` | no | `15m` | poll cadence |
| `SIGHUP_TIMEOUT` | no | `30s` | per-check timeout |
| `SIGHUP_WEBHOOK_SECRET` | no | *(unset = no auth)* | compared against the webhook header |
| `SIGHUP_WEBHOOK_HEADER` | no | `Authorization` | header carrying the secret |

`SIGHUP_TARGETS` is a JSON array, and each entry names one Deployment together with the moving tag
that drives it:

```json
[
  {"repository": "library/someapp", "tag": "edge", "namespace": "app", "deployment": "someapp"},
  {"repository": "library/other",   "tag": "stable", "namespace": "app", "deployment": "otherapp"}
]
```

A few notes on the fields:

- `repository`, `tag`, `namespace` and `deployment` are all required.
- `annotation` is optional, and defaults to `sighup/applied`.
- No two targets may name the same `namespace/deployment`. There is one writer per Deployment, and
  Sighup enforces that at startup, rather than letting you discover it later as an annotation race.

Digest resolution is anonymous for public images, whether that is a public Harbor project or a
public ghcr package, so no registry credentials are needed at all. The webhook side, though, is
**Harbor-specific**. Because ghcr does not send webhooks, a ghcr-only deployment simply rides on the
poll loop (or the `cronjob` mode), and the webhook endpoint never fires.

## HTTP surface (`serve`)

| Path | Purpose |
|---|---|
| `POST /hook` | Harbor webhook; `202` accepted (some target matched), `200` ignored, `401` bad secret |
| `GET /healthz` | liveness |
| `GET /readyz` | readiness |

You will want to configure a Harbor webhook on the `library` project. Set it up for `PUSH_ARTIFACT`,
over HTTP, using the **Default** payload format (the CloudEvents format is not handled), pointed at
the in-cluster Service, with an auth header set to the same value as `SIGHUP_WEBHOOK_SECRET`. One
webhook covers every target Sighup watches: a push is routed to the targets watching that `repo:tag`
(there may be several), and the rest are left alone. Because both Harbor and Sighup live in the
cluster, none of this needs a gateway, a certificate, or external DNS.

## RBAC

There is **one ServiceAccount per Sighup**, created in the namespace Sighup is installed into. That
single identity is bound into each target namespace by **one Role and one RoleBinding per target
namespace**. Each Role names only that namespace's Deployments:

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

Because Kubernetes lets a RoleBinding name a subject from another namespace, that one ServiceAccount
is bound into every target namespace. Widening the list of targets widens each Role's `resourceNames`
on purpose. It never widens to a wildcard.

## Chart

`charts/sighup/` can install Sighup either way. By default it runs the resident method: the
Deployment, a ClusterIP Service in front of the webhook, the ServiceAccount, and the RBAC, the last
of which creates one Role per namespace that pins each `target.deployment` **by name**. Flip to the
scheduled method with `cronjob.enabled=true` and `deployment.enabled=false`, and you get a CronJob
that runs the `cronjob` mode on a `schedule` instead, sharing the same ServiceAccount, RBAC, and
security defaults, with no Service and no webhook. Either way the scope belongs to the chart, which
means it is not something an operator can quietly widen later.

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

To install the CronJob shape instead of the Deployment:

```yaml
deployment:
  enabled: false
cronjob:
  enabled: true
  schedule: "*/15 * * * *"
```

Run one shape or the other, never both: two writers race the same annotation, and the lock is
per-process. The chart warns at install time if you enable both.

The webhook Secret is expected to come from outside the chart, through
`webhook.secret.existingSecret`, so it never lands in your Helm values, nor in the release Secret
where Helm stores them. `webhook.secret.create` exists only for standalone use, and the chart says
so.

```sh
make chart-lint    # helm lint --strict, with a throwaway target
make chart-guard   # asserts the chart still REFUSES to render without targets
```

There is no default target, and that is on purpose: a Sighup should know nothing about any
particular app. That means `targets` must be non-empty, and every target's `repository`, `tag` and
`deployment` are required. One small catch: `helm lint` only logs missing required values as
**warnings**, even under `--strict`, and it still exits 0. That is exactly why `make chart-guard`
exists, to prove the guards are still doing their job.

## Development

```sh
make test          # unit tests: a fake clientset + a stubbed digest source, no cluster needed
make run-cronjob   # run the `cronjob` mode against your current kubeconfig (export SIGHUP_REGISTRY + SIGHUP_TARGETS)
make docker-build
```

## Gotchas

- **Helm strips the annotation.** A `tofu apply` that re-renders a Deployment drops the
  `sighup/applied` annotation. That drop is itself a pod-template change, so it costs you one
  extra rollout, after which the next check re-applies the annotation. It is harmless, but it does
  mean a Helm upgrade causes two rolls.
- **A bad push goes live.** Whatever CI published under the tracked tag is what runs, and there is
  no human gate unless you build one. If that worries you, track a promoted tag (for example
  "stable") instead of the tag CI pushes every commit to, and then the gate becomes "who moves
  `stable`".
- **One writer per Deployment.** No two targets may name the same Deployment (Sighup refuses to
  start if they do), and you should not run a second Sighup against one this one already owns,
  because two writers race the same annotation.
- **One registry per Sighup.** `SIGHUP_REGISTRY` is shared by every target, so a second registry
  means a second Sighup.

