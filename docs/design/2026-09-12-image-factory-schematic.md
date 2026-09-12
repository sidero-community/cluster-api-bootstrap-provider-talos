# Image Factory schematic resolution in CABPT

Date: 2026-09-12. Status: approved design, awaiting implementation plan.

Companion to the talos2disk action design in `sidero-community/actions`
(`docs/superpowers/specs/2026-09-12-talos2disk-action-design.md`). That action
installs Talos from the Image Factory raw image and reads the schematic and
version it must install from `machine.install.image` in the machine
configuration. This document describes how CABPT puts that reference there.

## Goal

CABPT owns the Image Factory schematic of a machine: the operator declares the
schematic customization on the `TalosConfig` (or `TalosConfigTemplate`), CABPT
resolves a full Talos version, validates the extensions against what the
Factory offers for that version, registers the schematic, and renders
`machine.install.image` as `<factory host>/metal-installer/<schematic ID>:<version>`.
Install and every later upgrade therefore name the same schematic, and a
change to the schematic rolls out through the existing in-place update path
because the installer image is already part of the in-place config hash.

Agent actions do not talk to the Kubernetes API. Everything the install needs
reaches the machine inside the machine configuration.

## Decisions

| Question | Decision |
| --- | --- |
| Where the schematic is declared | `spec.imageFactory` on `TalosConfig`, a block mirroring the Factory schematic: extensions, extraKernelArgs, overlay, bootloader. Inherited by `TalosConfigTemplate` through its embedded spec. |
| Version for the installer tag | A full `spec.talosVersion` is used as is. A bare minor resolves to the newest non-prerelease patch the Factory serves, is recorded in status, and is re-resolved only when `spec.talosVersion` or the schematic block changes. |
| Extension compatibility | Every requested extension must appear in the Factory's official extension list for the resolved version; otherwise resolution fails with a condition naming the extension. The Factory picks the extension versions. |
| Where resolution runs | Inline in the TalosConfig reconciler before configuration generation, with in-memory caches; a Factory failure returns an error and requeues, so bootstrap data is never rendered with a wrong or missing image. |
| Infrastructure-provider installer image | The `status.installerImage` hook (`installerImageFor`) is removed. CABPT's own resolution is the only source of `machine.install.image`. |

## API

```yaml
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: TalosConfigTemplate
spec:
  template:
    spec:
      generateType: worker
      talosVersion: v1.14
      imageFactory:
        extensions:
          - siderolabs/nvme-cli
          - siderolabs/intel-ucode
        extraKernelArgs:
          - talos.logging.kernel=udp://10.0.0.5:514/
        overlay:
          name: rpi_generic
          image: ghcr.io/siderolabs/sbc-raspberrypi
        bootloader: sd-boot
```

`TalosConfigSpec` gains:

```go
// ImageFactory declares the Talos Image Factory schematic the machine installs and
// upgrades with. When set, CABPT registers the schematic and renders
// machine.install.image from it.
ImageFactory *ImageFactorySpec `json:"imageFactory,omitempty"`

type ImageFactorySpec struct {
	// Extensions are official system extension names, e.g. siderolabs/nvme-cli.
	// +optional
	Extensions []string `json:"extensions,omitempty"`
	// ExtraKernelArgs are baked into the images the Factory builds from the schematic.
	// +optional
	ExtraKernelArgs []string `json:"extraKernelArgs,omitempty"`
	// Overlay selects a single-board-computer overlay.
	// +optional
	Overlay *ImageFactoryOverlay `json:"overlay,omitempty"`
	// Bootloader selects the bootloader of the disk image.
	// +kubebuilder:validation:Enum=auto;dual-boot;grub;sd-boot
	// +optional
	Bootloader string `json:"bootloader,omitempty"`
}

type ImageFactoryOverlay struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}
```

`TalosConfigStatus` gains:

```go
// ImageFactory records the schematic CABPT resolved for this configuration.
// +optional
ImageFactory *ImageFactoryStatus `json:"imageFactory,omitempty"`

type ImageFactoryStatus struct {
	// TalosVersion is the full version the installer image is tagged with.
	TalosVersion string `json:"talosVersion,omitempty"`
	// SchematicID is the content-addressed ID the Factory returned.
	SchematicID string `json:"schematicID,omitempty"`
	// InstallerImage is the machine.install.image rendered into the configuration.
	InstallerImage string `json:"installerImage,omitempty"`
	// ObservedInputs is a hash of spec.talosVersion and spec.imageFactory; while it
	// matches the current spec the fields above are reused without asking the Factory.
	ObservedInputs string `json:"observedInputs,omitempty"`
}
```

Condition `ImageFactoryResolved` on `status.conditions` (metav1): `True` with
reason `Resolved`; `False` with reason `UnknownExtension`, `NoReleasedPatch`,
`InvalidOverlay` or `FactoryUnavailable` and a message naming the offending
value. CRD manifests are regenerated with `make generate manifests`.

## Resolution flow

Runs in the TalosConfig reconciler before `genConfigs`, for `generateType`
values that render a configuration (`controlplane`, `worker`, `init`) and for
`none` as well, since the patch applies to user-supplied data equally.

1. If `spec.imageFactory` is nil, do nothing: no install-image patch, no status, behaviour unchanged.
2. Compute `inputs = sha256(spec.talosVersion + canonical spec.imageFactory)`. If `status.imageFactory.observedInputs == inputs` and `installerImage` is set, reuse the status values. No Factory call.
3. Resolve the version. `vX.Y.Z[-pre]` is used as is. `vX.Y` calls `GET /versions`, keeps entries with prefix `vX.Y.` and no `-`, and takes the highest patch; none is `NoReleasedPatch`. Anything else is invalid.
4. Validate extensions: `GET /version/<version>/extensions/official`; every requested name must be present, else `UnknownExtension` naming the first missing one. Names are trimmed, deduplicated and sorted.
5. Register: `POST /schematics` with the schematic YAML (`customization.systemExtensions.officialExtensions`, `customization.extraKernelArgs`, `customization.bootloader`, `overlay.{name,image}`), `Content-Type: application/yaml`; the returned `id` is the schematic ID.
6. `installerImage = <factory host>/metal-installer/<id>:<version>`, where the host is the `--image-factory-url` without scheme.
7. Write `status.imageFactory` and the condition, then continue generation with `installImagePatch(installerImage)` as the first strategic patch, exactly as the removed hook did, and `renderedConfigHash(scope, installerImage)`.

Any Factory error sets the condition to `False` with `FactoryUnavailable` and
returns the error, so controller-runtime retries with backoff and the
bootstrap data secret is not written until the image is known. Spec errors
(`UnknownExtension`, `NoReleasedPatch`, `InvalidOverlay`) also return an
error so they surface in the controller log and the condition; they clear as
soon as the spec is corrected.

## Factory client

`internal/imagefactory` holds the client and the schematic model:

- `Client` over `net/http` with `Versions(ctx)`, `OfficialExtensions(ctx, version) ([]string, error)`, `CreateSchematic(ctx, Schematic) (id string, err error)`, `InstallerImage(id, version) string`.
- `Schematic{Overlay *Overlay; Customization{SystemExtensions{OfficialExtensions}, ExtraKernelArgs, Bootloader}}` marshalled with the Factory's field names.
- `Resolver` wraps the client with caches: schematic ID by marshalled body (unbounded, the set is small), versions and official-extension lists with a 10-minute TTL and stale-on-error fallback.
- An interface `ImageFactory` with the three calls is what the reconciler holds, so tests inject a fake.

`--image-factory-url` (default `https://factory.talos.dev`) in `main.go`
`InitFlags` builds the client; `TalosConfigReconciler.ImageFactory` receives
it. The manager fails at startup if the URL is not `http(s)`.

## What is removed

- `controllers/installer_image.go`: `installerImageFor` and `InstallerImageStatusField`. `installImagePatch` stays.
- `internal/inplace/updatemachine.go`: `installerImage()` reading `status.installerImage` from the InfraMachine. The in-place handler reads `status.imageFactory.installerImage` from the TalosConfig it already loads, so `desiredConfigHash` hashes the same image the renderer used.
- The MachinePool comment and the `renderedConfigHash(scope, "")` special case: the image is per TalosConfig, so pools hash and render it like Machines.
- `docs/in-place-updates.md` section on `status.installerImage`, replaced by a pointer to this document.

## Consequences for the rest of the system

- **talos2disk** reads the schematic ID and the version from `machine.install.image` and downloads `<factory>/image/<id>/<version>/metal-<arch>.raw.zst`. It no longer detects hardware, registers schematics or patches the Hardware. Its spec is revised accordingly.
- **CAPT** and the **runtime-extensions resolver** no longer need the `talos.tinkerbell.org/userdata-owner` yield introduced earlier today; both commits are reverted. The resolver itself and the Workflow gate stay as they are; the Template does not read their output, and `operating_system.slug`/`version` remain a last-resort fallback for the action when the configuration carries no Factory reference.
- **Hardware-specific extensions** are declared per TalosConfigTemplate (per MachineDeployment or control plane) rather than detected per machine. The `talos.tinkerbell.org/system-extensions` Hardware annotation is no longer consumed by the install path.

## Testing

- `internal/imagefactory`: httptest server covering `/versions`, `/version/<v>/extensions/official`, `POST /schematics` (201, 400 with message), the installer image string, cache reuse and stale-on-error.
- Resolution logic as a pure function `resolveImageFactory(spec, status, client) (ImageFactoryStatus, condition, error)`: reuse on matching inputs, full version passthrough, bare-minor resolution, unknown extension, no released patch, Factory error.
- Controller: the existing envtest integration suite gains a case with a fake `ImageFactory` asserting that the rendered bootstrap data carries `machine.install.image`, the status fields are set, the config hash changes when `extensions` changes, and a TalosConfig without the block renders no image.
- In-place: the existing `updatemachine` tests are adjusted for the new image source.
- `make generate manifests` output is committed; `make test` and `make lint` pass (`GOTOOLCHAIN=go1.26.1` for lint on a newer system Go).

## Out of scope

- Private or authenticated Image Factories (a single public URL flag).
- Validating `overlay.image` against the Factory beyond the Factory's own rejection.
- Re-resolving a pinned patch automatically; operators bump `talosVersion`.
