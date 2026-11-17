# Cluster API Bootstrap Provider Talos (CABPT)

<!-- textlint-disable -->
> [!CAUTION]
> Sidero Labs is no longer actively developing Cluster API Bootstrap Provider Talos.
>
> For an alternative, please see [Omni](https://github.com/siderolabs/omni.git).
>
> Unless you have an existing support contract covering Cluster API Bootstrap Provider Talos, all support will be provided by the community (including questions in our Slack workspace).
<!-- textlint-enable -->

## Intro

The Cluster API Bootstrap Provider Talos (CABPT) is a project by [Sidero Labs](https://siderolabs.com/) that provides a [Cluster API](https://github.com/kubernetes-sigs/cluster-api) (CAPI) bootstrap provider for use in deploying Talos-based Kubernetes nodes across any environment.
Given some basic info, this provider will generate bootstrap configurations for a given machine and reconcile the necessary custom resources for CAPI to pick up the generated data.

## Corequisites

There are a few corequisites and assumptions that go into using this project:

- [Cluster API](https://github.com/kubernetes-sigs/cluster-api)
- [Talos](https://talos.dev/)

## Installing

CABPT provider should be installed alongside with [CACPPT](https://github.com/sidero-community/cluster-api-control-plane-provider-talos) provider.

```shell
clusterctl init --bootstrap talos --control-plane talos --infrastructure <infrastructure provider>
```

If you encounter the following error, this is caused by a rename of our GitHub org from `talos-systems` to `siderolabs`.

```bash
$ clusterctl init -b talos -c talos -i sidero
Fetching providers
Error: failed to get provider components for the "talos" provider: target namespace can't be defaulted. Please specify a target namespace
```

This can be worked around by adding the following to `~/.cluster-api/clusterctl.yaml` and rerunning the init command:

```yaml
providers:
  - name: "talos"
    url: "https://github.com/sidero-community/cluster-api-bootstrap-provider-talos/releases/latest/bootstrap-components.yaml"
    type: "BootstrapProvider"
  - name: "talos"
    url: "https://github.com/sidero-community/cluster-api-control-plane-provider-talos/releases/latest/control-plane-components.yaml"
    type: "ControlPlaneProvider"
  - name: "sidero"
    url: "https://github.com/siderolabs/sidero/releases/latest/infrastructure-components.yaml"
    type: "InfrastructureProvider"
```

## Compatibility

This provider's versions are compatible with the following versions of Cluster API:

|                | v1alpha3 (v0.3) | v1alpha4 (v0.4) | v1beta1 (v1.x) | v1beta2 (v1.12+) |
| -------------- | --------------- | --------------- | -------------- | ---------------- |
| CABPT (v0.5.x) |                 |                 | ✓              |                  |
| CABPT (v0.6.x) |                 |                 | ✓              |                  |
| CABPT (v0.7.x) |                 |                 |                | ✓                |
| CABPT (v0.8.x) |                 |                 |                | ✓                |
| CABPT (v0.9.x) |                 |                 |                | ✓                |
| CABPT (v0.10.x) |                 |                 |                | ✓                |

This provider's versions are able to install and manage the following versions of Kubernetes:

|                | v1.19 | v1.20 | v1.21 | v1.22 | v1.23 | v1.24 | v1.25 | v1.26 | v1.27 | v1.28 | v1.29 | v1.30 | v1.31 | v1.32 | v1.33 | v1.34 | v1.35 | v1.36 | v1.37 |
| -------------- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- |
| CABPT (v0.5.x) | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |       |       |       |       |       |       |       |       |       |       |       |
| CABPT (v0.6.x) |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| CABPT (v0.7.x) |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| CABPT (v0.8.x) |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |       |
| CABPT (v0.9.x) |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| CABPT (v0.10.x) |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |

This provider's versions are compatible with the following versions of Talos:

|                  | v1.0  | v1.1  | v1.2  | v1.3  | v1.4  | v1.5  | v1.6  | v1.7  | v1.8  | v1.9  | v1.10 | v1.11 | v1.12 | v1.13 | v1.14 |
| ---------------- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- |
| CABPT (v0.5.x)   | ✓     | ✓     | ✓     | ✓     |       |       |       |       |       |       |       |       |       |       |       |
| CABPT (v0.6.x)   |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| CABPT (v0.7.x)   |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| CABPT (v0.8.x)   |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     |       |
| CABPT (v0.9.x)   |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     |
| CABPT (v0.10.x)  |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     |

CABPT generates machine configuration compatible with Talos Linux version specified in the `talosVersion:` field (see below).

## Usage

CABPT is not used directly, but rather via CACPPT (`TalosControlPlane`) for control plane nodes or via `MachineDeployment` (`MachinePool`) for worker nodes.
In either case, CABPT settings are passed via `TalosConfigTemplate` resource:

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: TalosControlPlane
metadata:
  name: mycluster-cp
spec:
  controlPlaneConfig:
    controlplane:
      generateType: controlplane
      talosVersion: v1.11
  ...
```

```yaml
apiVersion: bootstrap.cluster.x-k8s.io/v1alpha3
kind: TalosConfigTemplate
metadata:
  name: mycluster-workers
spec:
  template:
    spec:
      generateType: worker
      talosVersion: v1.9
```

Fields available in the `TalosConfigTemplate` (and `TalosConfig`) resources:

- `generateType`: Talos machine configuration type to generate (`controlplane`, `init` (deprecated), `worker`) or `none` for user-supplied configuration (see below)
- `talosVersion`: version of Talos to generate machine configuration for (e.g. `v1.0`, patch version might be omitted).
   CABPT defaults to the latest supported Talos version, but it can generate configuration compatible with previous versions of Talos.
   It is recommended to always set this field explicitly to avoid issues when CABPT is upgraded to the version which supports new Talos version.
- `configPatches` (optional): set of machine configuration patches to apply to the generated configuration.
- `data` (only for `generateType: none`): user-supplied machine configuration.
- `hostname` (optional): configure hostname in the generate machine configuration:
  - `source` (`MachineName`): set the hostname in the generated machine configuration to the `Machine` name (not supported with `MachinePool` deployments)
- `imageFactory` (optional): the [Talos Image Factory](https://factory.talos.dev) schematic the machine installs and upgrades with.
  CABPT registers the schematic, pins the Talos version (a bare minor in `talosVersion` resolves to the newest released patch once and is recorded in `status.imageFactory`), and renders `<factory>/metal-installer/<schematic>:<version>` as the installer image: an `UnattendedInstallConfig` document from Talos 1.14 (disk selector `system_disk`, override it with a strategic patch), `machine.install.image` before.
  The controller talks to the Factory at `--image-factory-url` (default `https://factory.talos.dev`). Fields:
  - `extensions`: official system extension names, e.g. `siderolabs/nvme-cli`; each must exist for the resolved version.
  - `extraKernelArgs`: kernel arguments baked into the Factory images.
  - `overlay`: `{name, image}` for single-board computers.
  - `bootloader`: `auto`, `dual-boot`, `grub` or `sd-boot`.

### Generated Machine Configuration

When `generateType` is set to the machine type of the Talos nodes (`controlplane` for control plane nodes and `worker` for worker nodes), CABPT generates a set of cluster-wide
secrets which are used to provision machine configuration for each node.
Machine configuration generated is compatible with the Talos version set in the `talosVersion` field.

```yaml
spec:
  generateType: controlplane
  talosVersion: v1.5
```

### User-supplied Machine Configuration

In this mode CABPT passes through machine configuration set in `data` field as bootstrap data to the `Machine`.
Machine configuration can be generated with `talosctl gen config`.

```yaml
spec:
  generateType: none
  data: |
    version: v1alpha1
    machine:
      type: controlplane
    ...
    ...
    ...
```

### Configuration Patches

Machine configuration can be customized by applying configuration patches.
Any field of the [Talos machine configuration](https://docs.siderolabs.com/talos/v1.14/reference/configuration/overview)
can be overridden on a per-machine basis using this method.

See [Talos Linux documentation](https://docs.siderolabs.com/talos/v1.14/configure-your-talos-cluster/system-configuration/patching) for more information on patching.

Example (following Talos 1.14+ syntax, see documentation for older versions for other syntax):

```yaml
spec:
  generateType: controlplane
  talosVersion: v1.14
  strategicPatches:
    - |
      apiVersion: v1alpha1
      kind: UnattendedInstallConfig
      provisioning:
        diskSelector:
            match: disk.dev_path == "/dev/sda"
    - |
      # disable Flannel CNI
      apiVersion: v1alpha1
      kind: KubeFlannelCNIConfig
      $patch: delete
```

### Restricting which machines can join

By default every worker configuration of a cluster carries the same Kubernetes bootstrap token, and it never expires.
A disk that still holds the configuration of a deleted `Machine` can therefore boot and join again, and Cluster API will take its Node for whatever `Machine` now sits on that hardware.

`restrictJoin` ties joining to the `Machine` in the management cluster:

```yaml
spec:
  generateType: worker
  restrictJoin: true
```

With it set, CABPT does two things.

- **Join ID.** The kubelet registers its Node with the label `bootstrap.cluster.x-k8s.io/talos-join-id`, set to the UID of the owning `Machine` (or `MachinePool`).
  CABPT keeps the join IDs of living owners in the ConfigMap `kube-system/talos-join-allowlist` of the workload cluster, and installs a `ValidatingAdmissionPolicy` (`talos-join.bootstrap.cluster.x-k8s.io`) that refuses a kubelet creating a Node with any other ID.
  The ID of a `Machine` leaves the allowlist when the `Machine` starts being deleted.
- **Join token.** A worker owned by a `Machine` gets a bootstrap token of its own in `cluster.token`, derived from the cluster's Kubernetes CA key and the `Machine` UID.
  Its Secret exists in the workload cluster only while the `Machine` lives and its Node is not ready, so the configuration of a deleted `Machine` cannot request a kubelet certificate.
  Control plane machines, `MachinePool` members and `generateType: none` keep the cluster-wide token.

The policy protects the cluster only once it covers every machine, and it is careful not to lock one out on the way there:

- A `Machine` without `restrictJoin` (or bootstrapped by another provider) is admitted by the name of its Node, once it has one.
- While any living `Machine` has neither a join ID nor a Node, and while any `MachinePool` is without `restrictJoin`, the policy only warns and audits.
  The allowlist's `bootstrap.cluster.x-k8s.io/talos-join-enforcement` annotation says `Deny` or `Warn`, and `talos-join-uncovered` names the machines holding it at `Warn`.
- Nothing is installed before the control plane is initialized, and everything is removed again when the last `TalosConfig` drops the field.

Set the field on the control plane and on every worker template to reach `Deny` and stay there.
On running machines it is delivered [in place](docs/in-place-updates.md); the Nodes they already have are not affected, since only the creation of a Node is checked.

What it does not do:

- It is not proof against impersonation. A machine holding the configuration of a living `Machine` can register as that `Machine`.
- It does not revoke kubelet certificates. A stale node that still holds one is stopped by the join ID, not by the token.
- The cluster-wide token stays valid for control plane machines, so worker configurations rendered before `restrictJoin` was set keep working as tokens until those machines are replaced.
- A node whose `Machine` has `restrictJoin` set but whose configuration has not been updated in place yet cannot register again if its Node is deleted in the meantime.

The policy needs Kubernetes 1.30 or later. CABPT reaches the workload cluster through the `<cluster>-kubeconfig` Secret.

### Retrieving `talosconfig`

Client-side `talosconfig` is required to access the cluster using Talos API.
CABPT generates `talosconfig` for generated machine configuration and stores it as `<cluster>-talosconfig` secret in cluster's namespace.

`talosconfig` can be retrieved with:

```shell
kubectl get secret --namespace <cluster-namespace> <cluster-name>-talosconfig -o jsonpath='{.data.talosconfig}' | base64 -d > cluster-talosconfig
talosctl config merge cluster-talosconfig
talosctl -n <IP> version
```

CABPT updates endpoints in the `talosconfig` based on control plane `Machine` addresses.

### Operation

CABPT reconciles `TalosConfig` resources.
Once `TalosConfig` and its associated `Machine` are ready, CABPT generates machine configuration and stores it in the `<machine>-bootstrap-data` Secret.
Kubernetes cluster CA is stored in the `<cluster>-ca` Secret.
Cluster-wide shared secrets are stored in the `<cluster>-talos` Secret.
Client-side Talos API configuration is stored in the `<cluster>-talosconfig` Secret.

As part of its operation, CABPT sets a number of Conditions on the `TalosConfig/Status` resource:

- `DataSecretAvailable`: CABPT generated machine configuration in `<machine>-bootstrap-data` Secret, CABPT unblocks infrastructure provider to boot the `Machine`.
- `ClientConfigAvailableCondition`: CABPT generated Talos client configuration in `<cluster>-talosconfig` Secret.

```shell
$ kubectl describe talosconfig talosconfig-cp-0
Status:
  Conditions:
    Last Transition Time:  2021-10-25T20:54:09Z
    Status:                True
    Type:                  Ready
    Last Transition Time:  2021-10-25T20:54:09Z
    Status:                True
    Type:                  ClientConfigAvailable
    Last Transition Time:  2021-10-25T20:54:09Z
    Status:                True
    Type:                  DataSecretAvailable
  Data Secret Name:        cp-0-bootstrap-data
```

If CABPT fails to perform its operations, it stores error message for the respective Condition.

```shell
Status:
  Conditions:
    Last Transition Time:  2021-10-25T20:54:09Z
    Message:               failure applying rfc6902 patches to talos machine config: add operation does not apply: doc is missing path: "/machine/time/servers": missing value
    Reason:                DataSecretGenerationFailed
    Severity:              Error
    Status:                False
```

These statuses are also presented in the `clusterctl describe cluster --show-conditions all` output.

### Limitations

Once CABPT has rendered a `Machine`'s configuration into its `<machine>-bootstrap-data` Secret, editing the `TalosConfig` spec does not re-render it.
Nothing re-reads that Secret after the machine has booted, so the change takes effect only for machines created afterwards, or through the [in-place update flow](docs/in-place-updates.md).
To roll it out to running machines, use an in-place update or replace the machines.

A `MachinePool` is different, because a pool keeps a single `<pool>-bootstrap-data` Secret that its infrastructure provider re-reads every time it creates an instance.
There a spec change **is** acted on: CABPT re-renders the Secret in place, keeping its name, and applies the result to the pool's running members over the Talos API, one node at a time.
This is CABPT's own behaviour rather than the Cluster API in-place update flow, which has no MachinePool path at all; see [in-place updates](docs/in-place-updates.md) for what it does and does not cover, and `--enable-machine-pool-in-place-updates` to turn it off.
It is on by default, so **upgrading to a build that has it applies the pool's current spec to every running member without prompting** — including a spec edit that was inert while the Secret was frozen, which may reboot each node in turn; read [Adopting it on an existing pool](docs/in-place-updates.md#adopting-it-on-an-existing-pool) before you upgrade.

## Building

This project can be built simply by running `make release` from the root directory.
Doing so will create a file called `_out/bootstrap-talos/<version>/bootstrap-components.yaml`.
If you wish, you can tweak settings by editing the release yaml.
This file can then be installed into your management cluster with `kubectl apply -f _out/bootstrap-components.yaml`.

## Support

Join our [Slack](https://slack.dev.talos-systems.io)!
