# In-place updates

Cluster API normally applies a Machine spec change by deleting the Machine and creating a
replacement. On bare metal that is expensive: a Talos patch bump, a `configPatches` edit, or
a Tinkerbell template change reprovisions every node from scratch.

Cluster API v1.12 added [in-place update extensions][proposal]. CABPT implements one for
Talos, so these changes can instead be applied to the running machine.

[proposal]: https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20240807-in-place-updates.md

## What can be updated in place

| Change | In place? | How |
|---|---|---|
| `TalosConfig.spec.configPatches` / `strategicPatches` / `data` / `hostname` | yes | config regenerated, applied with `ApplyConfiguration` in `AUTO` mode — Talos decides whether a reboot is needed |
| `TalosConfig.spec.talosVersion` | yes | as above; changes the config contract used for generation |
| `Machine.spec.version` (Kubernetes) | yes | the version feeds the control plane component and kubelet images in the machine config |
| Talos OS version (`machine.install.image`) | yes | Talos `Upgrade` API; Talos cordons and drains the node itself |
| `TalosConfig.spec.generateType` | **no** | a worker↔control plane role flip cannot be applied to a running node |
| InfrastructureMachine spec, by default | **no** | falls back to a rolling replacement |
| TinkerbellMachine template / affinity / ISO URL | yes | see below |

Anything not claimed by the extension falls back to a rolling replacement, which is the
current behaviour. The feature can only make Cluster API do *less* work, never more.

## Tinkerbell

Most of `TinkerbellMachine.spec` is consumed only when a machine is first provisioned and is
inert for one already running. CAPT short-circuits reconciliation once the Hardware carries
`HardwareProvisionedAnnotation=true` and only creates a Workflow when none exists, so it does
not re-image a running machine on a spec change.

The extension therefore absorbs these without touching the node at all:

- `spec.templateInline`, `spec.templateRef`
- `spec.hardwareAffinity`
- `spec.bootOptions.isoURL`

`spec.bootOptions.bootMode` is excluded, because it changes how the machine boots.
`spec.hardwareName` and `spec.providerID` are immutable in CAPT.

> **Behaviour change.** With in-place updates enabled, editing a Tink template no longer
> re-images running machines — it takes effect the next time a machine is actually
> provisioned. If you want the old behaviour for a particular change, roll the
> MachineDeployment or use the `OnDelete` strategy.

CAPT also power cycles a machine through Rufio if an in-place update stalls. Once Cluster API
commits to the in-place path there is no fallback to replacement, so a node that never
returns from an upgrade reboot would otherwise hang. One hard power cycle is attempted, after
which the update is left failed for an operator to inspect.

## Enabling it

**CABPT's side is on by default.** `--enable-runtime-extension` defaults to true, and the Service
and certificate ship in the default kustomization, so installing the provider is enough. Serving
the hooks is inert until Cluster API is told to call them, which is the remaining work below.

The `ExtensionConfig` that registers the server is written by the manager, not shipped as a
manifest. It is cluster scoped, so neither clusterctl nor the Cluster API operator rewrites the
install namespace inside `spec.clientConfig.service` the way they do for webhook configurations,
and cert-manager's ca-injector only patches webhook configurations, APIServices and CRDs — so
`cert-manager.io/inject-ca-from` on an `ExtensionConfig` is silently a no-op. A static manifest
would therefore name the wrong namespace with an empty `caBundle`. Cluster API's registry warmup
treats a non-discoverable extension as **fatal**, so that combination crash-loops the core
manager. The manager takes its namespace from the downward API and the CA from its mounted
serving certificate, and reconciles the object every 10 minutes so a rotated CA is picked up.

**1. Feature gates on the core Cluster API controllers**

Both are required. `InPlaceUpdates` enables the feature; `RuntimeSDK` enables the runtime
extension machinery it is delivered through. With `RuntimeSDK=false` the `ExtensionConfig` is
never discovered and Machines simply sit at `UpToDate: false` with nothing acting on them.

```sh
kubectl -n capi-system set env deployment/capi-controller-manager \
  EXP_INPLACE_UPDATES=true EXP_RUNTIME_SDK=true
```

Or set `--feature-gates=InPlaceUpdates=true,RuntimeSDK=true` on the manager.

**2. Feature gate on CACPPT**, so control plane Machines participate:

```sh
kubectl -n talos-control-plane-system patch deployment cacppt-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--feature-gates=InPlaceUpdates=true"}]'
```

Adjust the namespace if you did not install with the shipped manifests. Without this, worker
MachineDeployments still update in place — that path is handled entirely by core Cluster API —
but control plane machines keep rolling.

### Turning it off

Cluster API allows only **one** extension per hook, so this cannot coexist with another
`CanUpdateMachine` / `UpdateMachine` provider. To yield the hook, delete the `ExtensionConfig`
and pass `--enable-runtime-extension=false`:

```sh
kubectl delete extensionconfig cabpt-talos-in-place-updates
kubectl -n talos-bootstrap-system patch deployment cabpt-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--enable-runtime-extension=false"}]'
```

The flag must be turned off with the manifests, not on its own: the server needs the mounted
serving certificate and **the manager exits at startup if it is missing**. Equally, dropping the
`config/runtime-extension` manifests while leaving the flag on will crash-loop the manager.

## How it works

1. The owning controller (TalosControlPlane, or the MachineSet controller for workers) sees a
   Machine is outdated and calls `CanUpdateMachine`.
2. The extension replies with per-object JSON patches covering the changes it can absorb.
   Cluster API applies them to the current objects and compares against desired: a full match
   means in-place, anything left over means a rollout.
3. On a match the owner stamps `update-in-progress` on the Machine, InfraMachine and
   TalosConfig, writes the desired specs, and marks `UpdateMachine` pending.
4. CABPT's webhook admits the TalosConfig spec change because of that annotation, and
   regenerates the bootstrap data secret, stamping a hash of the inputs on it.
5. The core Machine controller polls `UpdateMachine`. The extension waits for that hash to
   match the desired spec — otherwise it would apply the pre-update config and wrongly report
   success — then applies the config and, if the installer image changed, upgrades Talos.
6. When the extension reports completion, Cluster API clears the annotations.

Control plane machines are updated **one at a time**, and only while etcd is healthy across
the whole control plane, because an in-place upgrade reboots the node.

## Limitations

- Talos OS downgrades are not supported in place.
- Rotating an infrastructure template still rolls control plane machines: a new InfraMachine
  is required, and this provider only produces one by creating a replacement.
- MachinePools are handled separately, by CABPT itself — see below.
- The Talos API interactions are covered by unit tests against a fake client. They have not
  been validated against real hardware — do that before relying on this in production.

## MachinePools

Everything above is the Cluster API in-place update flow, and none of it reaches a MachinePool.
Cluster API v1.12 defines no MachinePool hook shapes, pool Machines carry no
`spec.bootstrap.configRef` and an empty `dataSecretName` — so the core Machine controller's
in-place executor skips them unconditionally — and the machinepool controller hardcodes
`MachineUpToDate: True`. There is no plug point to implement.

A pool also cannot simply freeze its configuration the way a Machine does. It keeps **one**
`<pool>-bootstrap-data` Secret, and its infrastructure provider re-reads that Secret every time
it creates an instance, so a frozen Secret would strand the pool on the configuration it was
created with — including every instance created afterwards.

CABPT therefore owns this path itself. For a MachinePool-owned `TalosConfig` that is already
ready, each reconcile:

1. computes the hash of the inputs the machine configuration renders from — the `TalosConfig`
   spec and the pool's `spec.template.spec.version` — and compares it against the hash stamped
   on the Secret;
2. on a mismatch, re-renders and rewrites the **same** Secret in place, so the pool's bootstrap
   reference and every future instance stay valid;
3. lists the pool's Machines — the ones Cluster API labels
   `cluster.x-k8s.io/pool-name=<pool>` and `cluster.x-k8s.io/cluster-name=<cluster>` — and, for
   each that is not already recorded as running the current configuration, applies exactly the
   bytes in the Secret with `ApplyConfiguration` in `AUTO` mode;
4. records the applied hash on the Machine as
   `bootstrap.cluster.x-k8s.io/applied-config-hash`, so a requeue skips the members that have
   already converged;
5. reports progress on the `MachinePoolInPlaceUpdate` condition of the `TalosConfig`
   (`"2 of 3 MachinePool machines updated"`).

Members are updated **strictly one at a time**, and the pass stops at the first failure:
Cluster API is not driving this update, so nothing else would notice a bad configuration before
it reached the whole pool. The failure surfaces on the condition and the reconcile is retried
with backoff.

### Adopting it on an existing pool

> **Read this before upgrading.** The flag defaults to **true**, so upgrading to a build that
> has it takes effect on existing pools with no operator action.

On the first reconcile after the upgrade, **every existing member of every pool receives one
`ApplyConfiguration`**. No member carries `bootstrap.cluster.x-k8s.io/applied-config-hash` yet,
and an un-annotated member that is already current is indistinguishable from one that is stale
— so the only safe reading is that none of them have converged. For a pool whose spec has not
changed this is harmless: the bytes are identical, and an identical configuration is a no-op in
`AUTO` mode.

**Where it is not harmless is a pool whose `TalosConfig` spec was edited while the old behaviour
froze the Secret.** Such an edit — a `ClusterClass` bootstrap change admitted by the webhook, say
— was inert: it reached neither the Secret nor the nodes. On the first reconcile after the
upgrade it is re-rendered into the Secret and applied to every running member, one at a time, in
`AUTO` mode. Talos decides per change whether it can be applied to the running system, so
**each member may reboot**. Nothing prompts for this and nothing gates it.

Before upgrading, either reconcile the spec you actually want the pool to run, or opt out with
`--enable-machine-pool-in-place-updates=false` and turn it on deliberately later. To see what
would be applied, compare `<pool>-bootstrap-data` against a rendering of the current spec.

### What the MachinePool path does not do

- **No Talos upgrades.** `machine.install.image` changes are written into the configuration but
  no `Upgrade` call is made, because rebooting pool members is not something this controller can
  coordinate. Roll the pool to change the Talos version.
- **Installer image.** `machine.install.image` comes from `spec.imageFactory` on the TalosConfig and
  is rendered and hashed for a pool exactly as for a Machine.
- **Pool Machines are required.** Cluster API creates them only when the infrastructure provider
  publishes `status.infrastructureMachineKind` on its InfraMachinePool. Without them there is no
  supported way to find the pool's nodes; the condition says so, the Secret is still re-rendered,
  and the change reaches instances created from then on.
- This does not touch `MachineUpToDate` or anything else the machinepool controller owns.

### Turning the MachinePool path off

`--enable-machine-pool-in-place-updates` defaults to true. With `--enable-machine-pool-in-place-updates=false`
a pool behaves as it always has: the Secret is frozen once rendered and nothing talks to the
nodes. The flag is independent of `--enable-runtime-extension`; this path is not served through
the runtime extension at all.

## Installer image resolution

`machine.install.image` is rendered by CABPT itself from `spec.imageFactory` on the TalosConfig:
the schematic is registered with the Talos Image Factory, the Talos version is pinned in
`status.imageFactory.talosVersion`, and the resulting
`<factory>/metal-installer/<schematic>:<version>` is applied as a strategic patch ahead of any
`strategicPatches`, so an explicit patch still wins.

The image is part of the bootstrap data secret's config hash, so a change to the extension set
or a version bump changes the hash, `UpdateMachine` sees a stale secret until CABPT re-renders,
applies the new configuration and upgrades the node to the new image. The `UpdateMachine`
handler reads the image from the live TalosConfig status, because Cluster API strips status
from the hook request.

See `docs/design/2026-09-12-image-factory-schematic.md` for the resolution rules.
