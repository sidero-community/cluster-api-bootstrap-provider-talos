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

Three things must line up.

**1. Feature gate on the core Cluster API controllers**

```sh
clusterctl init --bootstrap talos --control-plane talos ...
kubectl -n capi-system set env deployment/capi-controller-manager \
  EXP_INPLACE_UPDATES=true
```

Or set `--feature-gates=InPlaceUpdates=true` on the manager.

**2. Feature gate on CACPPT**, so control plane Machines participate:

```sh
kubectl -n cacppt-system patch deployment cacppt-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--feature-gates=InPlaceUpdates=true"}]'
```

Without this, worker MachineDeployments still update in place — that path is handled entirely
by core Cluster API — but control plane machines keep rolling.

**3. The extension on CABPT**

Add `--enable-runtime-extension` to the manager and apply
`config/runtime-extension/runtime-extension.yaml`, which creates the Service, the
certificate, and the `ExtensionConfig` that points Cluster API at the server.

Cluster API currently allows only **one** extension per hook, so this cannot coexist with
another `CanUpdateMachine` / `UpdateMachine` provider.

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
- MachinePools are not supported.
- The Talos API interactions are covered by unit tests against a fake client. They have not
  been validated against real hardware — do that before relying on this in production.
