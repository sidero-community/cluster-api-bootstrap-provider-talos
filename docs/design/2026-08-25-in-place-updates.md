# In-place updates for the Talos CAPI providers

Status: approved design
Date: 2026-08-25

## Problem

Cluster API rolls out Machine spec changes by deleting the Machine and creating a
replacement. For Talos clusters on bare metal this is expensive and often
unnecessary:

- A Talos patch-version bump reprovisions every node from scratch, even though
  Talos can upgrade itself in place via its own `Upgrade` API.
- A change to `TalosConfig.spec.configPatches` reprovisions every node, even
  though Talos applies most machine-config changes without a reboot.
- On Tinkerbell, a change to the Tink template or boot options reprovisions every
  node, even though those fields only affect *initial* provisioning and are inert
  for a node that is already running.

Cluster API v1.12 introduced in-place update extensions, which let a provider
absorb some of these changes without replacing the Machine. Nothing in the Talos
provider stack implements them.

## Background: how CAPI v1.12 in-place updates work

Three hooks are defined in `sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1`,
served by a Runtime Extension registered as an `ExtensionConfig`. They are only
invoked when the `InPlaceUpdates` feature gate is enabled. **Only one extension
may register per hook**, which is a load-bearing constraint for this design.

The work splits into two halves.

**Owner half** — the controller that owns the Machine (KCP for control planes,
MachineSet for workers) notices a Machine is outdated and calls
`CanUpdateMachine` / `CanUpdateMachineSet`. The extension responds with per-object
JSON patches describing the changes it can absorb. CAPI applies those patches onto
the *current* objects and compares the result against *desired*:

- Equal → the extension covers the whole diff; proceed in-place.
- Not equal → fall back to a rolling replacement.

An extension therefore **declares capability by what it patches, and declines by
omission**. CAPI normalizes both sides with an SSA dry-run first so that
defaulting does not produce phantom diffs.

To trigger, the owner stamps `in-place-updates.internal.cluster.x-k8s.io/update-in-progress`
on the Machine, InfraMachine and BootstrapConfig, SSA-writes the desired specs,
and marks `UpdateMachine` pending via `runtime.cluster.x-k8s.io/pending-hooks`.

**Executor half** — the core Machine controller polls `UpdateMachine` until it
returns `retryAfterSeconds: 0`, then clears the annotations. This half is
provider-agnostic and already works for any bootstrap provider.

### Consequences for Talos

| Path | Owner half | Executor half |
|---|---|---|
| Workers (MachineDeployment/MachineSet) | core CAPI, already provider-agnostic | extension |
| Control plane (`TalosControlPlane`) | **must be written** — TCP is not KCP | extension |

Workers come nearly free. The control plane is the bulk of the work, because
`TalosControlPlane` has to replicate what `KubeadmControlPlane` does in
`controlplane/kubeadm/internal/controllers/inplace_*.go`.

## Repositories

| Repo | Role |
|---|---|
| `cluster-api-bootstrap-provider-talos` (CABPT) | hosts the Runtime Extension; owns TalosConfig semantics and machine-config generation |
| `cluster-api-control-plane-provider-talos` (CACPPT) | owner half for control-plane Machines |
| `cluster-api-provider-tinkerbell` (CAPT) | infra provider; supplies the inert-field story and BMC-backed recovery |
| `tinkerbell` | upstream CRDs (Hardware, Workflow, Rufio BMC). **No changes required.** |

## Architecture

### Why the extension lives in CABPT

CAPI permits only one extension per hook, so a single server must answer for the
Machine, the BootstrapConfig *and* the InfraMachine. CABPT is the right host: it
owns `TalosConfig` semantics and machine-config generation, and one server then
covers both control-plane and worker machines, since both bootstrap through
`TalosConfig`.

The InfraMachine is handled **generically**, without importing any infra
provider. See "Infra field allowlist" below.

### CABPT: `internal/inplace`

A runtime extension server built on `exp/runtime/server`, off by default behind
`--enable-runtime-extension`, listening on its own port with its own certificate.

- **`CanUpdateMachine` / `CanUpdateMachineSet`** — emit patches for
  `TalosConfig.spec` (`configPatches`, `strategicPatches`, `data`, `hostname`,
  `talosVersion`) and `Machine.spec.version`. Emit an InfraMachine patch only for
  field paths on the configured allowlist. Deliberately omit everything else,
  including `generateType`: a controlplane↔worker role flip must remain a
  replacement.

- **`UpdateMachine`** — the executor state machine, described below.

- **Webhook relaxation** — `TalosConfig.Spec` is immutable today
  (`api/v1beta1/talosconfig_webhook.go`), which hard-blocks the trigger step. The
  check is narrowed to permit a spec change *only* when the incoming object
  carries `UpdateInProgressAnnotation`. The trigger writes the annotation and the
  spec in the same admission request, so this is sufficient, and immutability is
  preserved for every other caller.

- **Data-secret regeneration** — the controller currently generates the bootstrap
  data secret once and never revisits it. When the in-place annotation is present
  and the observed generation is behind, it re-runs generation in place, reusing
  the same secret name and the existing cluster PKI, and stamps a hash of the
  rendered config so `UpdateMachine` can tell fresh from stale.

### CACPPT: owner half

- `controllers/inplace.go` — a `reconcileInPlaceUpdates` phase that runs in
  `reconcileMachines` immediately before the `MachinesNeedingRollout()` check and
  subtracts the Machines it claims. Chosen over teaching `MachinesNeedingRollout()`
  to consult the extension, which would force a pure collection filter to take a
  context, a client and a runtime client, and would be far harder to test.

- `controllers/inplace_trigger.go` — a port of KCP's trigger.

- `internal/hooks`, `internal/ssa` — small local ports. CAPI's equivalents live
  under `internal/` and are not importable out-of-tree. Roughly 50 and 30 lines
  respectively, carrying upstream attribution.

### Infra field allowlist

The extension never imports an infra provider. Instead the InfraMachine arrives as
a `runtime.RawExtension` and is compared field-path-wise against a configured
allowlist of paths that are **provisioning-time-only** — fields that affect how a
machine is first built but are inert for one already running.

The default policy is empty: unknown infra providers get no InfraMachine patch,
so any infra change falls back to replacement. That is the safe default and
preserves today's behaviour.

A Tinkerbell policy ships built in, allowlisting:

- `spec.templateInline`
- `spec.templateRef`
- `spec.hardwareAffinity`
- `spec.bootOptions.isoURL`

`spec.bootOptions.bootMode`, `spec.hardwareName` and `spec.providerID` are
deliberately excluded — the first changes how the machine boots, and the latter
two are immutable in CAPT anyway.

This is safe because of a verified property of CAPT: `machineReconcileScope.reconcile`
short-circuits when the Hardware carries `HardwareProvisionedAnnotation=true`, and
`ensureTemplateAndWorkflow` only creates a Workflow when none exists. **CAPT does
not re-provision an already-provisioned machine when its spec changes.** The
allowlisted fields are therefore genuinely inert, not merely tolerated.

The trade-off, which must be documented for users: after this change, editing a
Tink template no longer re-images running machines. It takes effect the next time
a machine is actually provisioned. Users who want re-imaging should roll the
MachineDeployment or use `OnDelete`.

### CAPT: BMC-backed recovery

An in-place Talos OS upgrade reboots the node. If it does not come back, the
update stalls indefinitely, because CAPI is committed to the in-place path once
`update-in-progress` is written.

CAPT adds a small assist controller that watches TinkerbellMachines whose owning
Machine carries `update-in-progress`. If the machine has not returned within a
configurable timeout, it issues a Rufio power-cycle `Job` — reusing the BMC
plumbing already present in `controller/machine/bmc.go` — and records the attempt
in a condition, so one recovery is attempted rather than an endless loop.

This lives in CAPT rather than the extension so the Tinkerbell dependency stays in
the Tinkerbell repo.

## `UpdateMachine` state machine

Idempotent by construction: every step observes current node state and acts only
on a difference. CAPI polls it repeatedly.

1. Resolve the node's Talos endpoint; build a client from the
   `<cluster>-talosconfig` secret.
2. Fetch the bootstrap data secret named by the desired Machine. If its config
   hash does not yet match the desired TalosConfig generation, return
   `retryAfterSeconds` and wait for CABPT to regenerate.
3. Compare the desired rendered config against the node's running config.
4. If `machine.install.image` differs, apply the config first, then drive the
   Talos `Upgrade` API with the desired installer image. Talos performs its own
   cordon and drain.
5. Otherwise, if the config differs, call `ApplyConfiguration` in `AUTO` mode and
   let Talos decide whether a reboot is required.
6. Poll until the node is Ready and, for control-plane machines, its etcd member
   is healthy. Return `retryAfterSeconds: 0` when settled.

## Safety

- **One control-plane machine at a time**, gated on the existing etcd health
  helpers in CACPPT's `etcd.go` and `health.go`. An in-place OS upgrade reboots
  the node, so this is a genuine quorum concern.
- **Failures surface, they do not silently fall back.** Once `update-in-progress`
  is written CAPI is committed; a hook failure is reported as
  `MachineInPlaceUpdateFailedReason` rather than being papered over.
- **Feature-gated** (`InPlaceUpdates`) in both providers, defaulting off, matching
  upstream. There is no per-object opt-in, matching KCP.
- **Conservative by omission.** Any field not explicitly handled causes the
  patch-and-compare to fail, which falls back to a rolling replacement — the
  current behaviour.

## Testing

- CABPT unit: the `CanUpdateMachine` decision table — accepts config patches and
  version bumps, accepts allowlisted infra paths, declines non-allowlisted infra
  paths and `generateType` flips.
- CABPT unit: the `UpdateMachine` state machine against a faked Talos client.
- CABPT envtest: the webhook admits a spec change with the annotation and rejects
  one without.
- CACPPT unit: the trigger writes the expected annotations and pending hook;
  `reconcileInPlaceUpdates` respects one-at-a-time and the etcd gate.
- CAPT unit: the recovery controller issues exactly one power cycle.

## Not verified here

There is no Talos cluster in this environment. Everything touching the Talos
machine API — `ApplyConfiguration` mode selection, `Upgrade` sequencing, and the
health-settling logic — is exercised against a fake, behind a narrow interface
defined for exactly that purpose. **This needs live validation on real hardware
before production use.** The same applies to the Rufio power-cycle path in CAPT.

## Out of scope

- MachinePool support.
- Multiple extensions per hook (CAPI does not support it yet).
- Changes to the upstream `tinkerbell` repo.
- Talos OS *downgrades*, which Talos does not support in place.
