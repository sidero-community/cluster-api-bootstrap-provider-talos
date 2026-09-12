# Image Factory Schematic Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Talos Image Factory schematic ownership into CABPT (`spec.imageFactory` on TalosConfig, rendered as `machine.install.image`), and strip talos2disk down to consuming that reference: no hardware detection, no schematic registration, no Kubernetes write-back.

**Architecture:** CABPT's TalosConfig reconciler resolves a pinned Talos version, validates the requested extensions against the Factory's per-version list, registers the schematic and feeds `<factory host>/metal-installer/<id>:<version>` into the existing install-image strategic patch and in-place config hash; status records what was resolved so steady state makes no Factory calls. talos2disk reads the schematic ID and version out of `machine.install.image` in the Hardware's userData (with env and Hardware `operating_system` fallbacks), builds the raw image URL and installs. The two "userdata-owner" yields added earlier in CAPT and the runtime-extensions resolver are reverted, and the chart Template drops the kubeconfig.

**Tech Stack:** Go 1.26 (CABPT go.mod 1.26.1, actions go.mod 1.26.3), controller-runtime, controller-gen v0.19.0, Talos machinery, `net/http` Factory client, Helm.

**Specs:**
- `/home/appkins/src/sidero-community/cluster-api-bootstrap-provider-talos/docs/design/2026-09-12-image-factory-schematic.md` (CABPT)
- `/home/appkins/src/sidero-community/actions/docs/superpowers/specs/2026-09-12-talos2disk-action-design.md` (revised action)

## Global Constraints

- Four repositories, each on its own branch; never push; commit after every task with a conventional prefix; no attribution lines.
  - CABPT `/home/appkins/src/sidero-community/cluster-api-bootstrap-provider-talos`, branch `image-factory-schematic` (exists, base `main`).
  - actions `/home/appkins/src/sidero-community/actions`, branch `schematic-from-config` (exists, base `main`).
  - runtime-extensions `/home/appkins/src/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell`, new branch `schematic-from-config` off `bmc-manager`.
  - CAPT `/home/appkins/src/tinkerbell-community/cluster-api-provider-tinkerbell`, new branch `revert-userdata-yield` off `remove-talos-schematic`.
- CABPT API field names exactly: `spec.imageFactory{extensions, extraKernelArgs, overlay{name,image}, bootloader}`, `status.imageFactory{talosVersion, schematicID, installerImage, observedInputs}`, condition `ImageFactoryResolved` with reasons `Resolved`, `UnknownExtension`, `NoReleasedPatch`, `InvalidSpec`, `FactoryUnavailable`.
- Bootloader values: `auto`, `dual-boot`, `grub`, `sd-boot` (empty allowed).
- Installer image form: `<factory host without scheme>/metal-installer/<schematic id>:<full version>`.
- Factory endpoints: `GET /versions`, `GET /version/<version>/extensions/official` (JSON array of objects with `name`), `POST /schematics` with `Content-Type: application/yaml` returning `{id, schematic}`.
- Flag `--image-factory-url`, default `https://factory.talos.dev`; the manager exits at startup if it is not an http(s) URL.
- CABPT codegen runs without Docker: `go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 object:headerFile=./hack/boilerplate.go.txt paths="./..."` and `go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 crd:crdVersions=v1 paths="./api/..." output:crd:dir=config/crd/bases output:webhook:dir=config/webhook webhook`.
- CABPT unit tests: `go test ./api/... ./controllers/... ./internal/inplace/... ./internal/imagefactory/...`. The envtest suite under `internal/integration` runs through `make test` in CI and needs a cluster; it is not part of this plan's inner loop.
- CABPT lint: `GOTOOLCHAIN=go1.26.1 make lint` if the system Go is newer than go.mod.
- talos2disk environment after this plan: `HARDWARE` (required), `SCHEMATIC_ID`, `TALOS_VERSION`, `DISK_SELECTOR`, `KERNEL_ARGS`, `FACTORY_URL` (derived default), `NETWORK_CONFIG`, `LINK_NAMING`, `STRIP_SIGNATURE`, `RETRY_DURATION_MINUTES`, `DRY_RUN`. Removed: `EXTENSIONS`, `OVERLAY`, `NVIDIA_EXTENSIONS`, `KUBECONFIG`.
- talos2disk schematic chain: `SCHEMATIC_ID`, then the ID in `machine.install.image`, then `metadata.instance.operating_system.slug`; none is an error. Factory URL chain: `FACTORY_URL`, then `https://<host of machine.install.image>`, then `https://factory.talos.dev`.
- The machine configuration in userData is never logged by any action.
- In the actions repo, `make formatters` needs `env -u GOBIN`; `make lint` pins the toolchain itself.
- Reverts: CAPT commit `6bc8da8` ("feat: leave Hardware user data alone when talos2disk owns it"), runtime-extensions commit `896f8ff` ("feat(resolver): keep the installer image talos2disk recorded").

---

## File structure

| Repository | Path | Responsibility |
| --- | --- | --- |
| CABPT | `api/v1beta1/talosconfig_types.go` | `ImageFactorySpec`, `ImageFactoryOverlay`, `ImageFactoryStatus`, new spec/status fields. |
| CABPT | `api/v1beta1/conditions.go` | `ImageFactoryResolved` condition and reasons. |
| CABPT | `api/v1beta1/imagefactory_validation.go`, `_test.go` | Shared field validation used by both webhooks. |
| CABPT | `api/v1beta1/talosconfig_webhook.go`, `talosconfigtemplate_webhook.go` | Call the validation. |
| CABPT | `api/v1beta1/zz_generated.deepcopy.go`, `config/crd/bases/*.yaml` | Regenerated. |
| CABPT | `internal/imagefactory/client.go`, `cache.go`, `*_test.go` | Factory HTTP client, schematic model, caching wrapper, `API` interface. |
| CABPT | `controllers/image_factory.go`, `image_factory_test.go` | Version, extension and schematic resolution against `API`; status and condition handling. |
| CABPT | `controllers/talosconfig_controller.go`, `controllers/machinepool_inplace.go` | Use the resolved image; `installer_image.go` shrinks to `installImagePatch`. |
| CABPT | `internal/inplace/updatemachine.go`, `_test.go` | Read the image from the TalosConfig status. |
| CABPT | `main.go` | `--image-factory-url` flag and wiring. |
| CABPT | `README.md`, `docs/in-place-updates.md` | Field docs and the replaced resolution section. |
| actions | `pkg/factory/*` | Trimmed to reference parsing, image URL, `/versions`, version resolution. |
| actions | `pkg/talosconfig/patch.go` (deleted), `pkg/detect` (deleted), `pkg/kube` (deleted) | Removed. |
| actions | `talos2disk/inputs.go`, `settings.go`, `run.go`, `deps.go`, tests, `README.md` | New contract. |
| runtime-extensions | `charts/.../files/talos-install-template.yaml`, `Chart.yaml`, `docs/runtime-extensions-migration.md`, `internal/resolve/*` | Drop kubeconfig; revert the resolver yield. |
| CAPT | `controller/machine/hardware.go`, `hardware_test.go` | Revert the userData yield. |

---

### Task 1: CABPT API types, validation and generated code

**Files (CABPT):**
- Modify: `api/v1beta1/talosconfig_types.go`, `api/v1beta1/conditions.go`, `api/v1beta1/talosconfig_webhook.go:90-110`, `api/v1beta1/talosconfigtemplate_webhook.go:28-56`
- Create: `api/v1beta1/imagefactory_validation.go`, `api/v1beta1/imagefactory_validation_test.go`
- Regenerate: `api/v1beta1/zz_generated.deepcopy.go`, `config/crd/bases/bootstrap.cluster.x-k8s.io_talosconfigs.yaml`, `config/crd/bases/bootstrap.cluster.x-k8s.io_talosconfigtemplates.yaml`

**Interfaces:**
- Produces: `bootstrapv1beta1.ImageFactorySpec{Extensions, ExtraKernelArgs []string; Overlay *ImageFactoryOverlay; Bootloader string}`, `bootstrapv1beta1.ImageFactoryOverlay{Name, Image string}`, `bootstrapv1beta1.ImageFactoryStatus{TalosVersion, SchematicID, InstallerImage, ObservedInputs string}`, `TalosConfigSpec.ImageFactory *ImageFactorySpec`, `TalosConfigStatus.ImageFactory *ImageFactoryStatus`, constants `ImageFactoryResolvedCondition`, `ImageFactoryResolvedReason`, `ImageFactoryUnknownExtensionReason`, `ImageFactoryNoReleasedPatchReason`, `ImageFactoryInvalidSpecReason`, `ImageFactoryUnavailableReason`, `ImageFactoryBootloaders []string`, `validateImageFactory(path *field.Path, spec *ImageFactorySpec) field.ErrorList`.

- [ ] **Step 1: Write the failing validation test**

`api/v1beta1/imagefactory_validation_test.go`:

```go
package v1beta1

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateImageFactory(t *testing.T) {
	path := field.NewPath("spec", "imageFactory")

	if errs := validateImageFactory(path, nil); len(errs) != 0 {
		t.Fatalf("nil block must be valid, got %v", errs)
	}

	valid := &ImageFactorySpec{
		Extensions:      []string{"siderolabs/nvme-cli", "siderolabs/intel-ucode"},
		ExtraKernelArgs: []string{"talos.logging.kernel=udp://10.0.0.5:514/"},
		Overlay:         &ImageFactoryOverlay{Name: "rpi_generic", Image: "ghcr.io/siderolabs/sbc-raspberrypi"},
		Bootloader:      "sd-boot",
	}
	if errs := validateImageFactory(path, valid); len(errs) != 0 {
		t.Fatalf("valid block rejected: %v", errs)
	}

	tests := []struct {
		name string
		spec *ImageFactorySpec
		want string
	}{
		{"blank extension", &ImageFactorySpec{Extensions: []string{" "}}, "spec.imageFactory.extensions[0]"},
		{"extension with space", &ImageFactorySpec{Extensions: []string{"siderolabs/nvme cli"}}, "spec.imageFactory.extensions[0]"},
		{"duplicate extension", &ImageFactorySpec{Extensions: []string{"a/b", "a/b"}}, "spec.imageFactory.extensions[1]"},
		{"overlay without image", &ImageFactorySpec{Overlay: &ImageFactoryOverlay{Name: "x"}}, "spec.imageFactory.overlay.image"},
		{"overlay without name", &ImageFactorySpec{Overlay: &ImageFactoryOverlay{Image: "x"}}, "spec.imageFactory.overlay.name"},
		{"bad bootloader", &ImageFactorySpec{Bootloader: "uboot"}, "spec.imageFactory.bootloader"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateImageFactory(path, tt.spec)
			if len(errs) != 1 || !strings.HasPrefix(errs[0].Field, tt.want) {
				t.Fatalf("errs = %v, want one error at %s", errs, tt.want)
			}
		})
	}
}

func TestTalosConfigValidateCoversImageFactory(t *testing.T) {
	cfg := &TalosConfig{Spec: TalosConfigSpec{GenerateType: "worker", ImageFactory: &ImageFactorySpec{Bootloader: "uboot"}}}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "bootloader") {
		t.Fatalf("expected a bootloader error, got %v", err)
	}

	tpl := &TalosConfigTemplate{Spec: TalosConfigTemplateSpec{Template: TalosConfigTemplateResource{Spec: TalosConfigSpec{GenerateType: "worker", ImageFactory: &ImageFactorySpec{Extensions: []string{""}}}}}}
	if err := tpl.validate(); err == nil || !strings.Contains(err.Error(), "extensions") {
		t.Fatalf("expected an extensions error, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/appkins/src/sidero-community/cluster-api-bootstrap-provider-talos && go test ./api/v1beta1/ -run 'TestValidateImageFactory|TestTalosConfigValidateCoversImageFactory'`
Expected: FAIL to compile, undefined `validateImageFactory`, `ImageFactorySpec`.

- [ ] **Step 3: Add the types**

In `api/v1beta1/talosconfig_types.go`, replace

```go
	// Set hostname in the machine configuration to some value.
	Hostname HostnameSpec `json:"hostname,omitempty"`
	// Important: Run "make" to regenerate code after modifying this file
}
```

with

```go
	// Set hostname in the machine configuration to some value.
	Hostname HostnameSpec `json:"hostname,omitempty"`

	// ImageFactory declares the Talos Image Factory schematic the machine installs and
	// upgrades with. When set, CABPT registers the schematic with the Factory and renders
	// machine.install.image as <factory>/metal-installer/<schematic>:<version>.
	// +optional
	ImageFactory *ImageFactorySpec `json:"imageFactory,omitempty"`
	// Important: Run "make" to regenerate code after modifying this file
}

// ImageFactorySpec mirrors the Talos Image Factory schematic customization.
type ImageFactorySpec struct {
	// Extensions are official system extension names, e.g. siderolabs/nvme-cli. The
	// Factory picks the extension versions matching the Talos version.
	// +optional
	Extensions []string `json:"extensions,omitempty"`

	// ExtraKernelArgs are baked into the images the Factory builds from the schematic.
	// +optional
	ExtraKernelArgs []string `json:"extraKernelArgs,omitempty"`

	// Overlay selects a single-board-computer overlay.
	// +optional
	Overlay *ImageFactoryOverlay `json:"overlay,omitempty"`

	// Bootloader selects the bootloader of the disk image; the Factory default when empty.
	// +kubebuilder:validation:Enum=auto;dual-boot;grub;sd-boot
	// +optional
	Bootloader string `json:"bootloader,omitempty"`
}

// ImageFactoryOverlay is the Factory overlay block.
type ImageFactoryOverlay struct {
	// Name is the overlay name, e.g. rpi_generic.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Image is the overlay container image, e.g. ghcr.io/siderolabs/sbc-raspberrypi.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
}

// ImageFactoryStatus records the schematic CABPT resolved for this configuration.
type ImageFactoryStatus struct {
	// TalosVersion is the full version the installer image is tagged with.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// SchematicID is the content-addressed ID the Factory returned.
	// +optional
	SchematicID string `json:"schematicID,omitempty"`

	// InstallerImage is the machine.install.image rendered into the configuration.
	// +optional
	InstallerImage string `json:"installerImage,omitempty"`

	// ObservedInputs is a hash of spec.talosVersion and spec.imageFactory. While it matches
	// the current spec the fields above are reused without asking the Factory.
	// +optional
	ObservedInputs string `json:"observedInputs,omitempty"`
}
```

and replace

```go
	// DataSecretName is the name of the secret that stores the bootstrap data script.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DataSecretName string `json:"dataSecretName,omitempty"`
```

with

```go
	// DataSecretName is the name of the secret that stores the bootstrap data script.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DataSecretName string `json:"dataSecretName,omitempty"`

	// ImageFactory records the schematic resolved from spec.imageFactory.
	// +optional
	ImageFactory *ImageFactoryStatus `json:"imageFactory,omitempty"`
```

Append to `api/v1beta1/conditions.go`:

```go

// ImageFactoryResolved reports whether the schematic declared in spec.imageFactory was
// registered with the Talos Image Factory and turned into an installer image.
const (
	// ImageFactoryResolvedCondition is the condition type.
	ImageFactoryResolvedCondition = "ImageFactoryResolved"

	// ImageFactoryResolvedReason: the installer image in status.imageFactory is current.
	ImageFactoryResolvedReason = "Resolved"

	// ImageFactoryUnknownExtensionReason: an extension is not offered for the Talos version.
	ImageFactoryUnknownExtensionReason = "UnknownExtension"

	// ImageFactoryNoReleasedPatchReason: the bare minor in talosVersion has no released patch.
	ImageFactoryNoReleasedPatchReason = "NoReleasedPatch"

	// ImageFactoryInvalidSpecReason: talosVersion or the block is not usable.
	ImageFactoryInvalidSpecReason = "InvalidSpec"

	// ImageFactoryUnavailableReason: the Factory could not be reached or answered an error.
	ImageFactoryUnavailableReason = "FactoryUnavailable"
)
```

- [ ] **Step 4: Write the shared validation**

`api/v1beta1/imagefactory_validation.go`:

```go
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ImageFactoryBootloaders are the values the Factory accepts for customization.bootloader.
var ImageFactoryBootloaders = []string{"auto", "dual-boot", "grub", "sd-boot"}

// validateImageFactory checks the parts of an imageFactory block the CRD schema cannot:
// extension names, overlay completeness and the bootloader value. A nil block is valid.
func validateImageFactory(path *field.Path, spec *ImageFactorySpec) field.ErrorList {
	var errs field.ErrorList

	if spec == nil {
		return errs
	}

	seen := map[string]bool{}

	for i, ext := range spec.Extensions {
		p := path.Child("extensions").Index(i)

		switch {
		case strings.TrimSpace(ext) == "" || strings.ContainsAny(ext, " \t\n"):
			errs = append(errs, field.Invalid(p, ext, "extension names must be non-empty and contain no whitespace, e.g. siderolabs/nvme-cli"))
		case seen[ext]:
			errs = append(errs, field.Duplicate(p, ext))
		}

		seen[ext] = true
	}

	if o := spec.Overlay; o != nil {
		if o.Name == "" {
			errs = append(errs, field.Required(path.Child("overlay", "name"), "overlay needs a name"))
		}

		if o.Image == "" {
			errs = append(errs, field.Required(path.Child("overlay", "image"), "overlay needs an image"))
		}
	}

	if spec.Bootloader != "" {
		known := false

		for _, b := range ImageFactoryBootloaders {
			if b == spec.Bootloader {
				known = true
			}
		}

		if !known {
			errs = append(errs, field.NotSupported(path.Child("bootloader"), spec.Bootloader, ImageFactoryBootloaders))
		}
	}

	return errs
}
```

- [ ] **Step 5: Wire the validation into both webhooks**

In `api/v1beta1/talosconfig_webhook.go` `validate()`, after the hostname `switch` and before `if len(allErrs) == 0 {`, insert:

```go
	allErrs = append(allErrs, validateImageFactory(field.NewPath("spec").Child("imageFactory"), r.Spec.ImageFactory)...)
```

In `api/v1beta1/talosconfigtemplate_webhook.go`:

- change `ValidateCreate` to `return nil, r.validate()` after `r = obj`;
- change the end of `ValidateUpdate` to `return nil, r.validate()`;
- add:

```go
func (r *TalosConfigTemplate) validate() error {
	allErrs := validateImageFactory(field.NewPath("spec", "template", "spec", "imageFactory"), r.Spec.Template.Spec.ImageFactory)
	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: GroupVersion.Group, Kind: "TalosConfigTemplate"},
		r.Name, allErrs)
}
```

and add the imports `"k8s.io/apimachinery/pkg/runtime/schema"` and `"k8s.io/apimachinery/pkg/util/validation/field"` (keep `apierrors` which is already imported; run `go build ./api/...` and add whatever the compiler reports missing).

- [ ] **Step 6: Regenerate deepcopy and CRDs, run the tests**

```bash
cd /home/appkins/src/sidero-community/cluster-api-bootstrap-provider-talos
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 object:headerFile=./hack/boilerplate.go.txt paths="./..."
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 crd:crdVersions=v1 paths="./api/..." output:crd:dir=config/crd/bases output:webhook:dir=config/webhook webhook
grep -c "imageFactory" config/crd/bases/bootstrap.cluster.x-k8s.io_talosconfigs.yaml config/crd/bases/bootstrap.cluster.x-k8s.io_talosconfigtemplates.yaml
go build ./... && go test ./api/...
```

Expected: both CRDs mention `imageFactory` (the talosconfigs CRD twice: spec and status), build clean, `ok` for `api/v1beta1`. If controller-gen leaves unrelated diffs in `config/webhook` or the CRDs (marker ordering from a different generator version), inspect them with `git diff --stat`; only keep files that changed because of the new fields.

- [ ] **Step 7: Commit**

```bash
git add api config
git commit -m "feat(api): declare the Image Factory schematic on TalosConfig"
```

---

### Task 2: Image Factory client with caching

**Files (CABPT):**
- Create: `internal/imagefactory/client.go`, `internal/imagefactory/cache.go`, `internal/imagefactory/client_test.go`, `internal/imagefactory/cache_test.go`

**Interfaces:**
- Produces: `imagefactory.Schematic{Overlay *Overlay; Customization Customization}`, `imagefactory.Overlay{Image, Name string}`, `imagefactory.Customization{SystemExtensions SystemExtensions; ExtraKernelArgs []string; Bootloader string}`, `imagefactory.SystemExtensions{OfficialExtensions []string}`, `(Schematic).Marshal() ([]byte, error)`; interface `imagefactory.API{Versions(ctx) ([]string, error); OfficialExtensions(ctx, version string) ([]string, error); CreateSchematic(ctx, s Schematic) (string, error); InstallerImage(id, version string) string}`; `imagefactory.NewClient(baseURL string, httpClient *http.Client) (*Client, error)`; `imagefactory.DefaultURL = "https://factory.talos.dev"`; `imagefactory.NewCached(api API, ttl time.Duration) *Cached` implementing `API`; `imagefactory.IsFactoryError(err error) bool` (transport or non-2xx, as opposed to a spec problem).

- [ ] **Step 1: Write the failing client tests**

`internal/imagefactory/client_test.go`:

```go
package imagefactory

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newServer(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/versions":
			_, _ = w.Write([]byte(`["v1.13.9","v1.14.0","v1.14.2","v1.14.1","v1.15.0-alpha.1"]`))
		case r.Method == http.MethodGet && r.URL.Path == "/version/v1.14.2/extensions/official":
			_, _ = w.Write([]byte(`[{"name":"siderolabs/nvme-cli","ref":"ghcr.io/siderolabs/nvme-cli:v2.11","digest":"sha256:aa","author":"Sidero Labs","description":"nvme"},{"name":"siderolabs/intel-ucode","ref":"ghcr.io/siderolabs/intel-ucode:20250812","digest":"sha256:bb"}]`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/version/") && strings.HasSuffix(r.URL.Path, "/extensions/official"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/schematics":
			if r.Header.Get("Content-Type") != "application/yaml" {
				http.Error(w, "content type", http.StatusUnsupportedMediaType)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "siderolabs/does-not-exist") {
				http.Error(w, `{"error":"unknown extension siderolabs/does-not-exist"}`, http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testID + `","schematic":"` + strings.ReplaceAll(string(body), "\n", "\\n") + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSchematicMarshal(t *testing.T) {
	s := Schematic{
		Overlay: &Overlay{Name: "rpi_generic", Image: "ghcr.io/siderolabs/sbc-raspberrypi"},
		Customization: Customization{
			SystemExtensions: SystemExtensions{OfficialExtensions: []string{"siderolabs/amd-ucode", "siderolabs/nvme-cli"}},
			ExtraKernelArgs:  []string{"vga=791"},
			Bootloader:       "sd-boot",
		},
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := "overlay:\n    image: ghcr.io/siderolabs/sbc-raspberrypi\n    name: rpi_generic\ncustomization:\n    systemExtensions:\n        officialExtensions:\n            - siderolabs/amd-ucode\n            - siderolabs/nvme-cli\n    extraKernelArgs:\n        - vga=791\n    bootloader: sd-boot\n"
	if string(out) != want {
		t.Fatalf("Marshal() =\n%s\nwant\n%s", out, want)
	}
	empty, _ := Schematic{}.Marshal()
	if string(empty) != "customization: {}\n" {
		t.Fatalf("empty = %q", empty)
	}
}

func TestClientCalls(t *testing.T) {
	srv := newServer(t, nil)
	c, err := NewClient(srv.URL+"/", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	versions, err := c.Versions(ctx)
	if err != nil || len(versions) != 5 {
		t.Fatalf("Versions() = %v, %v", versions, err)
	}

	exts, err := c.OfficialExtensions(ctx, "v1.14.2")
	if err != nil || strings.Join(exts, ",") != "siderolabs/intel-ucode,siderolabs/nvme-cli" {
		t.Fatalf("OfficialExtensions() = %v, %v", exts, err)
	}
	if _, err := c.OfficialExtensions(ctx, "v9.9.9"); err == nil || !IsFactoryError(err) {
		t.Fatalf("unknown version must be a factory error, got %v", err)
	}

	id, err := c.CreateSchematic(ctx, Schematic{Customization: Customization{SystemExtensions: SystemExtensions{OfficialExtensions: []string{"siderolabs/nvme-cli"}}}})
	if err != nil || id != testID {
		t.Fatalf("CreateSchematic() = %q, %v", id, err)
	}
	_, err = c.CreateSchematic(ctx, Schematic{Customization: Customization{SystemExtensions: SystemExtensions{OfficialExtensions: []string{"siderolabs/does-not-exist"}}}})
	if err == nil || !strings.Contains(err.Error(), "unknown extension") || !IsFactoryError(err) {
		t.Fatalf("expected the factory message to surface, got %v", err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")
	if got := c.InstallerImage(testID, "v1.14.2"); got != host+"/metal-installer/"+testID+":v1.14.2" {
		t.Fatalf("InstallerImage() = %q", got)
	}
}

func TestNewClientRejectsBadURLs(t *testing.T) {
	for _, bad := range []string{"", "factory.talos.dev", "ftp://x", "http://"} {
		if _, err := NewClient(bad, nil); err == nil {
			t.Errorf("NewClient(%q) accepted", bad)
		}
	}
	if _, err := NewClient(DefaultURL, nil); err != nil {
		t.Fatal(err)
	}
}
```

`internal/imagefactory/cache_test.go`:

```go
package imagefactory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeAPI struct {
	versions   []string
	extensions map[string][]string
	ids        map[string]string
	err        error
	calls      map[string]int
}

func (f *fakeAPI) count(name string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeAPI) Versions(context.Context) ([]string, error) {
	f.count("versions")
	return f.versions, f.err
}

func (f *fakeAPI) OfficialExtensions(_ context.Context, version string) ([]string, error) {
	f.count("extensions")
	return f.extensions[version], f.err
}

func (f *fakeAPI) CreateSchematic(_ context.Context, s Schematic) (string, error) {
	f.count("create")
	if f.err != nil {
		return "", f.err
	}
	body, _ := s.Marshal()
	return f.ids[string(body)], nil
}

func (f *fakeAPI) InstallerImage(id, version string) string {
	return "factory.example.test/metal-installer/" + id + ":" + version
}

func TestCachedReusesWithinTTLAndServesStaleOnError(t *testing.T) {
	s := Schematic{Customization: Customization{SystemExtensions: SystemExtensions{OfficialExtensions: []string{"a/b"}}}}
	body, _ := s.Marshal()
	f := &fakeAPI{versions: []string{"v1.14.0"}, extensions: map[string][]string{"v1.14.0": {"a/b"}}, ids: map[string]string{string(body): "id1"}}
	c := NewCached(f, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := c.Versions(ctx); err != nil || len(v) != 1 {
			t.Fatal(v, err)
		}
		if e, err := c.OfficialExtensions(ctx, "v1.14.0"); err != nil || len(e) != 1 {
			t.Fatal(e, err)
		}
		if id, err := c.CreateSchematic(ctx, s); err != nil || id != "id1" {
			t.Fatal(id, err)
		}
	}
	if f.calls["versions"] != 1 || f.calls["extensions"] != 1 || f.calls["create"] != 1 {
		t.Fatalf("expected one upstream call each, got %v", f.calls)
	}

	f.err = errors.New("factory down")
	if v, err := c.Versions(ctx); err != nil || len(v) != 1 {
		t.Fatalf("stale versions must be served on error, got %v, %v", v, err)
	}
	if _, err := c.OfficialExtensions(ctx, "v1.15.0"); err == nil {
		t.Fatal("an uncached key has nothing stale to serve")
	}
	if id, err := c.CreateSchematic(ctx, s); err != nil || id != "id1" {
		t.Fatalf("schematic ids are content addressed and never expire, got %q, %v", id, err)
	}
	if c.InstallerImage("x", "v1") != "factory.example.test/metal-installer/x:v1" {
		t.Fatal("InstallerImage must pass through")
	}
}

func TestCachedExpiresLists(t *testing.T) {
	f := &fakeAPI{versions: []string{"v1.14.0"}}
	c := NewCached(f, time.Millisecond)
	ctx := context.Background()
	if _, err := c.Versions(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.Versions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.calls["versions"] != 2 {
		t.Fatalf("expected a refresh after the TTL, got %d calls", f.calls["versions"])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/imagefactory/`
Expected: FAIL, package has no non-test files.

- [ ] **Step 3: Write client.go**

```go
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package imagefactory talks to the Talos Image Factory: version listing,
// official extension listing and schematic registration.
package imagefactory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultURL is the public Image Factory.
const DefaultURL = "https://factory.talos.dev"

const maxBody = 1 << 20

// API is what the reconciler needs from the Factory. Client and Cached implement it;
// tests supply fakes.
type API interface {
	Versions(ctx context.Context) ([]string, error)
	OfficialExtensions(ctx context.Context, version string) ([]string, error)
	CreateSchematic(ctx context.Context, s Schematic) (string, error)
	InstallerImage(id, version string) string
}

// Schematic is the Factory schematic document; field names follow the Factory's own type.
type Schematic struct {
	Overlay       *Overlay      `yaml:"overlay,omitempty"`
	Customization Customization `yaml:"customization"`
}

// Overlay is the single-board-computer overlay block.
type Overlay struct {
	Image string `yaml:"image"`
	Name  string `yaml:"name"`
}

// Customization is the schematic customization block.
type Customization struct {
	SystemExtensions SystemExtensions `yaml:"systemExtensions,omitempty"`
	ExtraKernelArgs  []string         `yaml:"extraKernelArgs,omitempty"`
	Bootloader       string           `yaml:"bootloader,omitempty"`
}

// SystemExtensions lists official extensions.
type SystemExtensions struct {
	OfficialExtensions []string `yaml:"officialExtensions,omitempty"`
}

// Marshal renders the schematic as YAML for POST /schematics.
func (s Schematic) Marshal() ([]byte, error) {
	out, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshalling schematic: %w", err)
	}

	return out, nil
}

// FactoryError marks a failure talking to the Factory (transport or non-2xx), as
// opposed to a problem with what was asked of it.
type FactoryError struct {
	Err error
}

func (e *FactoryError) Error() string { return e.Err.Error() }
func (e *FactoryError) Unwrap() error { return e.Err }

// IsFactoryError reports whether err came from the Factory itself.
func IsFactoryError(err error) bool {
	var fe *FactoryError

	return errors.As(err, &fe)
}

// Client calls the Image Factory HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient validates baseURL (http or https with a host) and returns a client. A nil
// httpClient uses http.DefaultClient.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("image factory URL %q must be an http(s) URL", baseURL)
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{baseURL: strings.TrimRight(u.String(), "/"), http: httpClient}, nil
}

// Versions lists the Talos versions the Factory serves (broken ones excluded).
func (c *Client) Versions(ctx context.Context) ([]string, error) {
	raw, err := c.get(ctx, "/versions")
	if err != nil {
		return nil, err
	}

	var versions []string
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, &FactoryError{Err: fmt.Errorf("decoding /versions: %w", err)}
	}

	return versions, nil
}

// OfficialExtensions lists the official extension names available for version, sorted.
func (c *Client) OfficialExtensions(ctx context.Context, version string) ([]string, error) {
	raw, err := c.get(ctx, "/version/"+url.PathEscape(version)+"/extensions/official")
	if err != nil {
		return nil, err
	}

	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, &FactoryError{Err: fmt.Errorf("decoding extensions for %s: %w", version, err)}
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name != "" {
			names = append(names, e.Name)
		}
	}

	sortStrings(names)

	return names, nil
}

// CreateSchematic registers s and returns its content-addressed ID.
func (c *Client) CreateSchematic(ctx context.Context, s Schematic) (string, error) {
	body, err := s.Marshal()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/schematics", bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/yaml")
	req.Header.Set("Accept", "application/json")

	raw, err := c.do(req)
	if err != nil {
		return "", err
	}

	var reg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil || reg.ID == "" {
		return "", &FactoryError{Err: fmt.Errorf("factory returned no schematic id: %s", strings.TrimSpace(string(raw)))}
	}

	return reg.ID, nil
}

// InstallerImage is the machine.install.image reference for a schematic and version.
func (c *Client) InstallerImage(id, version string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(c.baseURL, "https://"), "http://")

	return fmt.Sprintf("%s/metal-installer/%s:%s", host, id, version)
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")

	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &FactoryError{Err: err}
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, &FactoryError{Err: fmt.Errorf("reading factory response: %w", err)}
	}

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &FactoryError{Err: fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, res.Status, strings.TrimSpace(string(raw)))}
	}

	return raw, nil
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}
```

Check which YAML module CABPT already depends on with `grep -n "yaml" go.mod`; if `gopkg.in/yaml.v3` is not a direct dependency but `sigs.k8s.io/yaml` or `go.yaml.in/yaml/v3` is, use that import instead (all three produce the same 4-space layout the test expects for these structs). Run `go mod tidy` after adding the import.

- [ ] **Step 4: Write cache.go**

```go
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package imagefactory

import (
	"context"
	"sync"
	"time"
)

// Cached wraps an API with in-memory caches: schematic IDs by body (content addressed,
// never expire), version and extension lists with a TTL and stale-on-error fallback.
type Cached struct {
	api API
	ttl time.Duration
	now func() time.Time

	mu         sync.Mutex
	schematics map[string]string
	versions   *listEntry
	extensions map[string]*listEntry
}

type listEntry struct {
	values  []string
	fetched time.Time
}

// NewCached returns a caching wrapper around api.
func NewCached(api API, ttl time.Duration) *Cached {
	return &Cached{api: api, ttl: ttl, now: time.Now, schematics: map[string]string{}, extensions: map[string]*listEntry{}}
}

// Versions implements API.
func (c *Cached) Versions(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.refresh(c.versions, func() ([]string, error) { return c.api.Versions(ctx) })
	if err != nil {
		return nil, err
	}

	c.versions = entry

	return entry.values, nil
}

// OfficialExtensions implements API.
func (c *Cached) OfficialExtensions(ctx context.Context, version string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.refresh(c.extensions[version], func() ([]string, error) { return c.api.OfficialExtensions(ctx, version) })
	if err != nil {
		return nil, err
	}

	c.extensions[version] = entry

	return entry.values, nil
}

// CreateSchematic implements API; a schematic body registered once is never re-sent.
func (c *Cached) CreateSchematic(ctx context.Context, s Schematic) (string, error) {
	body, err := s.Marshal()
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	id, ok := c.schematics[string(body)]
	c.mu.Unlock()

	if ok {
		return id, nil
	}

	id, err = c.api.CreateSchematic(ctx, s)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.schematics[string(body)] = id
	c.mu.Unlock()

	return id, nil
}

// InstallerImage implements API.
func (c *Cached) InstallerImage(id, version string) string {
	return c.api.InstallerImage(id, version)
}

// refresh returns entry when it is fresh, otherwise fetches; on a fetch error a stale
// entry is returned instead of the error.
func (c *Cached) refresh(entry *listEntry, fetch func() ([]string, error)) (*listEntry, error) {
	if entry != nil && c.now().Sub(entry.fetched) < c.ttl {
		return entry, nil
	}

	values, err := fetch()
	if err != nil {
		if entry != nil {
			return entry, nil
		}

		return nil, err
	}

	return &listEntry{values: values, fetched: c.now()}, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go mod tidy && go test ./internal/imagefactory/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/imagefactory
git commit -m "feat: Image Factory client with cached versions, extensions and schematics"
```

---

### Task 3: Schematic and version resolution

**Files (CABPT):**
- Create: `controllers/image_factory.go`, `controllers/image_factory_test.go`

**Interfaces:**
- Consumes: `imagefactory.API`, `imagefactory.Schematic`, `imagefactory.IsFactoryError`; `bootstrapv1beta1.ImageFactorySpec`, `ImageFactoryStatus`, the `ImageFactory*Reason` constants.
- Produces: `imageFactoryInputs(spec bootstrapv1beta1.TalosConfigSpec) string`, `schematicFromSpec(spec *bootstrapv1beta1.ImageFactorySpec) imagefactory.Schematic`, `resolveTalosVersion(ctx, api, raw string) (string, error)`, `resolveImageFactory(ctx context.Context, api imagefactory.API, spec bootstrapv1beta1.TalosConfigSpec, current *bootstrapv1beta1.ImageFactoryStatus) (*bootstrapv1beta1.ImageFactoryStatus, error)`, `imageFactoryReason(err error) string`, error type `*imageFactoryError{Reason string; Err error}`.

- [ ] **Step 1: Write the failing tests**

`controllers/image_factory_test.go`:

```go
package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/imagefactory"
)

type fakeFactory struct {
	versions   []string
	extensions map[string][]string
	err        error
	created    []imagefactory.Schematic
	calls      int
}

func (f *fakeFactory) Versions(context.Context) ([]string, error) {
	f.calls++
	return f.versions, f.err
}

func (f *fakeFactory) OfficialExtensions(_ context.Context, version string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	exts, ok := f.extensions[version]
	if !ok {
		return nil, &imagefactory.FactoryError{Err: errors.New("404 no such version")}
	}
	return exts, nil
}

func (f *fakeFactory) CreateSchematic(_ context.Context, s imagefactory.Schematic) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	f.created = append(f.created, s)
	return "sid-" + strings.Join(s.Customization.SystemExtensions.OfficialExtensions, "+"), nil
}

func (f *fakeFactory) InstallerImage(id, version string) string {
	return "factory.example.test/metal-installer/" + id + ":" + version
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{
		versions:   []string{"v1.13.9", "v1.14.0", "v1.14.2", "v1.14.1", "v1.15.0-alpha.1"},
		extensions: map[string][]string{"v1.14.2": {"siderolabs/intel-ucode", "siderolabs/nvme-cli"}, "v1.14.1": {"siderolabs/nvme-cli"}},
	}
}

func specWith(version string, extensions ...string) bootstrapv1beta1.TalosConfigSpec {
	return bootstrapv1beta1.TalosConfigSpec{
		GenerateType: "worker",
		TalosVersion: version,
		ImageFactory: &bootstrapv1beta1.ImageFactorySpec{Extensions: extensions},
	}
}

func TestResolveImageFactoryBareMinorAndFullVersion(t *testing.T) {
	f := newFakeFactory()
	ctx := context.Background()

	st, err := resolveImageFactory(ctx, f, specWith("v1.14", "siderolabs/nvme-cli", "siderolabs/intel-ucode", "siderolabs/nvme-cli"), nil)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if st.TalosVersion != "v1.14.2" || st.SchematicID != "sid-siderolabs/intel-ucode+siderolabs/nvme-cli" {
		t.Fatalf("status = %+v", st)
	}
	if st.InstallerImage != "factory.example.test/metal-installer/sid-siderolabs/intel-ucode+siderolabs/nvme-cli:v1.14.2" {
		t.Fatalf("installer = %q", st.InstallerImage)
	}
	if st.ObservedInputs == "" || st.ObservedInputs != imageFactoryInputs(specWith("v1.14", "siderolabs/nvme-cli", "siderolabs/intel-ucode", "siderolabs/nvme-cli")) {
		t.Fatalf("observedInputs = %q", st.ObservedInputs)
	}
	if len(f.created) != 1 || strings.Join(f.created[0].Customization.SystemExtensions.OfficialExtensions, ",") != "siderolabs/intel-ucode,siderolabs/nvme-cli" {
		t.Fatalf("schematic sent = %+v", f.created)
	}

	full, err := resolveImageFactory(ctx, f, specWith("v1.14.1", "siderolabs/nvme-cli"), nil)
	if err != nil || full.TalosVersion != "v1.14.1" {
		t.Fatalf("full version must pass through, got %+v, %v", full, err)
	}
}

func TestResolveImageFactoryReusesStatusWhileInputsMatch(t *testing.T) {
	f := newFakeFactory()
	spec := specWith("v1.14", "siderolabs/nvme-cli")
	first, err := resolveImageFactory(context.Background(), f, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.calls

	again, err := resolveImageFactory(context.Background(), f, spec, first)
	if err != nil || again.InstallerImage != first.InstallerImage || f.calls != calls {
		t.Fatalf("unchanged inputs must reuse status without Factory calls: %+v, %v, calls %d -> %d", again, err, calls, f.calls)
	}

	f.versions = append(f.versions, "v1.14.3")
	f.extensions["v1.14.3"] = []string{"siderolabs/nvme-cli"}
	same, _ := resolveImageFactory(context.Background(), f, spec, first)
	if same.TalosVersion != "v1.14.2" {
		t.Fatalf("a newer patch must not move a pinned version, got %s", same.TalosVersion)
	}

	changed := specWith("v1.14", "siderolabs/nvme-cli", "siderolabs/intel-ucode")
	moved, err := resolveImageFactory(context.Background(), f, changed, first)
	if err != nil || moved.TalosVersion != "v1.14.3" || moved.SchematicID == first.SchematicID {
		t.Fatalf("changed inputs must re-resolve, got %+v, %v", moved, err)
	}
}

func TestResolveImageFactoryErrors(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		f      *fakeFactory
		spec   bootstrapv1beta1.TalosConfigSpec
		reason string
	}{
		{"unknown extension", newFakeFactory(), specWith("v1.14.2", "siderolabs/nope"), bootstrapv1beta1.ImageFactoryUnknownExtensionReason},
		{"no released patch", newFakeFactory(), specWith("v1.15", "siderolabs/nvme-cli"), bootstrapv1beta1.ImageFactoryNoReleasedPatchReason},
		{"invalid version", newFakeFactory(), specWith("latest", "siderolabs/nvme-cli"), bootstrapv1beta1.ImageFactoryInvalidSpecReason},
		{"empty version", newFakeFactory(), specWith("", "siderolabs/nvme-cli"), bootstrapv1beta1.ImageFactoryInvalidSpecReason},
		{"factory down", &fakeFactory{err: &imagefactory.FactoryError{Err: errors.New("dial tcp: refused")}}, specWith("v1.14", "siderolabs/nvme-cli"), bootstrapv1beta1.ImageFactoryUnavailableReason},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := resolveImageFactory(ctx, tt.f, tt.spec, nil)
			if err == nil || st != nil {
				t.Fatalf("expected an error, got %+v", st)
			}
			if got := imageFactoryReason(err); got != tt.reason {
				t.Fatalf("reason = %q (%v), want %q", got, err, tt.reason)
			}
		})
	}

	if _, err := resolveImageFactory(ctx, newFakeFactory(), bootstrapv1beta1.TalosConfigSpec{TalosVersion: "v1.14"}, nil); err != nil {
		t.Fatalf("a spec without imageFactory must resolve to nothing without error, got %v", err)
	}
}

func TestSchematicFromSpec(t *testing.T) {
	s := schematicFromSpec(&bootstrapv1beta1.ImageFactorySpec{
		Extensions:      []string{" siderolabs/nvme-cli ", "siderolabs/amd-ucode", "siderolabs/nvme-cli"},
		ExtraKernelArgs: []string{"vga=791"},
		Overlay:         &bootstrapv1beta1.ImageFactoryOverlay{Name: "rpi_generic", Image: "ghcr.io/siderolabs/sbc-raspberrypi"},
		Bootloader:      "sd-boot",
	})
	if strings.Join(s.Customization.SystemExtensions.OfficialExtensions, ",") != "siderolabs/amd-ucode,siderolabs/nvme-cli" {
		t.Fatalf("extensions = %v", s.Customization.SystemExtensions.OfficialExtensions)
	}
	if s.Overlay == nil || s.Overlay.Name != "rpi_generic" || s.Customization.Bootloader != "sd-boot" || len(s.Customization.ExtraKernelArgs) != 1 {
		t.Fatalf("schematic = %+v", s)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./controllers/ -run 'TestResolveImageFactory|TestSchematicFromSpec'`
Expected: FAIL to compile, undefined `resolveImageFactory`.

- [ ] **Step 3: Write image_factory.go**

```go
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/imagefactory"
)

var (
	fullTalosVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.\-]+)?$`)
	bareTalosMinor   = regexp.MustCompile(`^v\d+\.\d+$`)
)

// imageFactoryError carries the condition reason for a resolution failure.
type imageFactoryError struct {
	Reason string
	Err    error
}

func (e *imageFactoryError) Error() string { return e.Err.Error() }
func (e *imageFactoryError) Unwrap() error { return e.Err }

// imageFactoryReason maps a resolution error onto an ImageFactoryResolved condition reason.
func imageFactoryReason(err error) string {
	var ife *imageFactoryError
	if errors.As(err, &ife) {
		return ife.Reason
	}

	if imagefactory.IsFactoryError(err) {
		return bootstrapv1beta1.ImageFactoryUnavailableReason
	}

	return bootstrapv1beta1.ImageFactoryUnavailableReason
}

// imageFactoryInputs hashes everything that determines the resolved image: the requested
// version and the schematic block. Encoding is JSON of a fixed-order struct, so it is stable.
func imageFactoryInputs(spec bootstrapv1beta1.TalosConfigSpec) string {
	encoded, _ := json.Marshal(struct {
		TalosVersion string                            `json:"talosVersion"`
		ImageFactory *bootstrapv1beta1.ImageFactorySpec `json:"imageFactory"`
	}{TalosVersion: spec.TalosVersion, ImageFactory: spec.ImageFactory})

	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:])
}

// schematicFromSpec builds the Factory schematic: extensions trimmed, deduplicated and
// sorted so identical specs yield identical bodies.
func schematicFromSpec(spec *bootstrapv1beta1.ImageFactorySpec) imagefactory.Schematic {
	seen := map[string]bool{}

	var extensions []string

	for _, ext := range spec.Extensions {
		ext = strings.TrimSpace(ext)
		if ext == "" || seen[ext] {
			continue
		}

		seen[ext] = true
		extensions = append(extensions, ext)
	}

	sort.Strings(extensions)

	s := imagefactory.Schematic{Customization: imagefactory.Customization{
		SystemExtensions: imagefactory.SystemExtensions{OfficialExtensions: extensions},
		ExtraKernelArgs:  spec.ExtraKernelArgs,
		Bootloader:       spec.Bootloader,
	}}

	if spec.Overlay != nil {
		s.Overlay = &imagefactory.Overlay{Name: spec.Overlay.Name, Image: spec.Overlay.Image}
	}

	return s
}

// resolveTalosVersion returns the full version for raw: a full version as is, a bare minor
// as its newest released patch known to the Factory.
func resolveTalosVersion(ctx context.Context, api imagefactory.API, raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	if fullTalosVersion.MatchString(raw) {
		return raw, nil
	}

	if !bareTalosMinor.MatchString(raw) {
		return "", &imageFactoryError{Reason: bootstrapv1beta1.ImageFactoryInvalidSpecReason,
			Err: fmt.Errorf("talosVersion %q must be a full version (v1.14.2) or a minor (v1.14) to build an installer image", raw)}
	}

	versions, err := api.Versions(ctx)
	if err != nil {
		return "", err
	}

	prefix := raw + "."
	best, bestPatch := "", -1

	for _, v := range versions {
		if !strings.HasPrefix(v, prefix) || strings.Contains(v, "-") {
			continue
		}

		patch, err := strconv.Atoi(strings.TrimPrefix(v, prefix))
		if err != nil {
			continue
		}

		if patch > bestPatch {
			best, bestPatch = v, patch
		}
	}

	if best == "" {
		return "", &imageFactoryError{Reason: bootstrapv1beta1.ImageFactoryNoReleasedPatchReason,
			Err: fmt.Errorf("the Image Factory serves no released patch of %s", raw)}
	}

	return best, nil
}

// resolveImageFactory turns spec.imageFactory into the status block holding the installer
// image. It returns (nil, nil) when the spec has no block, reuses current while its
// observedInputs match the spec, and otherwise resolves against the Factory.
func resolveImageFactory(ctx context.Context, api imagefactory.API, spec bootstrapv1beta1.TalosConfigSpec, current *bootstrapv1beta1.ImageFactoryStatus) (*bootstrapv1beta1.ImageFactoryStatus, error) {
	if spec.ImageFactory == nil {
		return nil, nil
	}

	inputs := imageFactoryInputs(spec)

	if current != nil && current.ObservedInputs == inputs && current.InstallerImage != "" {
		reused := *current

		return &reused, nil
	}

	version, err := resolveTalosVersion(ctx, api, spec.TalosVersion)
	if err != nil {
		return nil, err
	}

	schematic := schematicFromSpec(spec.ImageFactory)

	if want := schematic.Customization.SystemExtensions.OfficialExtensions; len(want) > 0 {
		available, err := api.OfficialExtensions(ctx, version)
		if err != nil {
			return nil, err
		}

		offered := map[string]bool{}
		for _, name := range available {
			offered[name] = true
		}

		for _, name := range want {
			if !offered[name] {
				return nil, &imageFactoryError{Reason: bootstrapv1beta1.ImageFactoryUnknownExtensionReason,
					Err: fmt.Errorf("extension %q is not offered by the Image Factory for Talos %s", name, version)}
			}
		}
	}

	id, err := api.CreateSchematic(ctx, schematic)
	if err != nil {
		return nil, err
	}

	return &bootstrapv1beta1.ImageFactoryStatus{
		TalosVersion:   version,
		SchematicID:    id,
		InstallerImage: api.InstallerImage(id, version),
		ObservedInputs: inputs,
	}, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./controllers/ -run 'TestResolveImageFactory|TestSchematicFromSpec'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add controllers/image_factory.go controllers/image_factory_test.go
git commit -m "feat: resolve the Image Factory schematic, version and installer image"
```

---

### Task 4: Wire resolution into the TalosConfig reconciler

**Files (CABPT):**
- Modify: `controllers/talosconfig_controller.go` (reconciler struct `:69-85`, deferred patch `:167-185`, `reconcileGenerate` `:447-458`), `controllers/installer_image.go` (delete `InstallerImageStatusField` and `installerImageFor`), `controllers/machinepool_inplace.go:157-165`
- Create: `controllers/image_factory_reconcile_test.go`

**Interfaces:**
- Consumes: `resolveImageFactory`, `imageFactoryReason` (Task 3), `imagefactory.API` (Task 2), `bootstrapv1beta1.ImageFactoryResolvedCondition` and reasons (Task 1).
- Produces: `TalosConfigReconciler.ImageFactory imagefactory.API`; `(*TalosConfigReconciler).resolveInstallerImage(ctx, config *bootstrapv1beta1.TalosConfig) (string, error)`; `installerImageFromStatus(config *bootstrapv1beta1.TalosConfig) string`.

- [ ] **Step 1: Write the failing reconciler test**

`controllers/image_factory_reconcile_test.go`:

```go
package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/imagefactory"
)

// imageFactoryFixture is a Machine-owned TalosConfig in a provisioned cluster, reconciled
// against a fake Image Factory.
type imageFactoryFixture struct {
	client     client.Client
	reconciler *TalosConfigReconciler
	factory    *fakeFactory
	key        types.NamespacedName
}

func newImageFactoryFixture(t *testing.T, spec bootstrapv1beta1.TalosConfigSpec, factory *fakeFactory) *imageFactoryFixture {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, capiv1.AddToScheme(scheme))
	require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))

	const owner = "machine-1"

	objects := []client.Object{
		&capiv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: testClusterName, Namespace: testNamespace},
			Spec:       capiv1.ClusterSpec{ControlPlaneEndpoint: capiv1.APIEndpoint{Host: "1.2.3.4", Port: 6443}},
			Status:     capiv1.ClusterStatus{Initialization: capiv1.ClusterInitializationStatus{InfrastructureProvisioned: ptr.To(true)}},
		},
		&capiv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: owner, Namespace: testNamespace, UID: "owner-uid"},
			Spec:       capiv1.MachineSpec{ClusterName: testClusterName, Version: "v1.34.0"},
		},
		&bootstrapv1beta1.TalosConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: owner, Namespace: testNamespace,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: capiv1.GroupVersion.String(), Kind: "Machine", Name: owner, UID: "owner-uid"}},
			},
			Spec: spec,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&bootstrapv1beta1.TalosConfig{}).WithObjects(objects...).Build()

	var api imagefactory.API
	if factory != nil {
		api = factory
	}

	return &imageFactoryFixture{
		client: c,
		reconciler: &TalosConfigReconciler{
			Client:       c,
			Log:          ctrl.Log.WithName("test"),
			Scheme:       scheme,
			ImageFactory: api,
		},
		factory: factory,
		key:     types.NamespacedName{Namespace: testNamespace, Name: owner},
	}
}

func (f *imageFactoryFixture) reconcile(t *testing.T) error {
	t.Helper()

	_, err := f.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key})

	return err
}

func (f *imageFactoryFixture) config(t *testing.T) *bootstrapv1beta1.TalosConfig {
	t.Helper()

	cfg := &bootstrapv1beta1.TalosConfig{}
	require.NoError(t, f.client.Get(context.Background(), f.key, cfg))

	return cfg
}

// bootstrapData returns the rendered machine configuration from the data secret.
func (f *imageFactoryFixture) bootstrapData(t *testing.T) string {
	t.Helper()

	cfg := f.config(t)
	require.NotEmpty(t, cfg.Status.DataSecretName, "bootstrap data must have been written")

	secret := &corev1.Secret{}
	require.NoError(t, f.client.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: cfg.Status.DataSecretName}, secret))

	return string(secret.Data["value"])
}

func TestReconcileRendersTheResolvedInstallerImage(t *testing.T) {
	t.Parallel()

	factory := newFakeFactory()
	f := newImageFactoryFixture(t, specWith("v1.14", "siderolabs/nvme-cli"), factory)

	require.NoError(t, f.reconcile(t))

	cfg := f.config(t)
	require.NotNil(t, cfg.Status.ImageFactory)
	assert.Equal(t, "v1.14.2", cfg.Status.ImageFactory.TalosVersion)
	assert.Equal(t, "sid-siderolabs/nvme-cli", cfg.Status.ImageFactory.SchematicID)
	assert.Equal(t, "factory.example.test/metal-installer/sid-siderolabs/nvme-cli:v1.14.2", cfg.Status.ImageFactory.InstallerImage)

	cond := meta.FindStatusCondition(cfg.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, bootstrapv1beta1.ImageFactoryResolvedReason, cond.Reason)

	data := f.bootstrapData(t)
	assert.Contains(t, data, "image: factory.example.test/metal-installer/sid-siderolabs/nvme-cli:v1.14.2", "machine.install.image must be rendered")

	secret := &corev1.Secret{}
	require.NoError(t, f.client.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: cfg.Status.DataSecretName}, secret))
	wantHash, err := bootstrapv1beta1.InPlaceConfigHash(cfg.Spec, "1.34.0", cfg.Status.ImageFactory.InstallerImage)
	require.NoError(t, err)
	assert.Equal(t, wantHash, secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation], "the hash must include the resolved image")
}

func TestReconcileWithoutImageFactoryBlockRendersNoImage(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.14"}, nil)

	require.NoError(t, f.reconcile(t))

	cfg := f.config(t)
	assert.Nil(t, cfg.Status.ImageFactory)
	assert.Nil(t, meta.FindStatusCondition(cfg.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition))
	assert.False(t, strings.Contains(f.bootstrapData(t), "metal-installer"), "no image must be injected without the block")
}

func TestReconcileFailsClosedWhenTheFactoryIsDown(t *testing.T) {
	t.Parallel()

	factory := &fakeFactory{err: &imagefactory.FactoryError{Err: errors.New("dial tcp: connection refused")}}
	f := newImageFactoryFixture(t, specWith("v1.14", "siderolabs/nvme-cli"), factory)

	require.Error(t, f.reconcile(t))

	cfg := f.config(t)
	assert.Empty(t, cfg.Status.DataSecretName, "no bootstrap data may be written without the image")
	assert.False(t, ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false))

	cond := meta.FindStatusCondition(cfg.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, bootstrapv1beta1.ImageFactoryUnavailableReason, cond.Reason)
	assert.Contains(t, cond.Message, "connection refused")
}

func TestReconcileReportsUnknownExtensions(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, specWith("v1.14.2", "siderolabs/nope"), newFakeFactory())

	require.Error(t, f.reconcile(t))

	cond := meta.FindStatusCondition(f.config(t).Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)
	require.NotNil(t, cond)
	assert.Equal(t, bootstrapv1beta1.ImageFactoryUnknownExtensionReason, cond.Reason)
	assert.Contains(t, cond.Message, "siderolabs/nope")
}

func TestReconcileRequiresAClientWhenTheBlockIsSet(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, specWith("v1.14.2", "siderolabs/nvme-cli"), nil)

	err := f.reconcile(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image-factory-url")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./controllers/ -run 'TestReconcile(Renders|Without|Fails|Reports|Requires)'`
Expected: FAIL to compile, unknown field `ImageFactory` in `TalosConfigReconciler`.

- [ ] **Step 3: Add the field and the resolution method**

In `controllers/talosconfig_controller.go`, in `TalosConfigReconciler` after `NodeClientFactory`:

```go

	// ImageFactory registers the schematic declared in spec.imageFactory and resolves the
	// Talos version for the installer image. Required when any TalosConfig sets the block;
	// nil is only acceptable for deployments that never do.
	ImageFactory imagefactory.API
```

Add the import `"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/imagefactory"` and `"k8s.io/apimachinery/pkg/api/meta"`.

In the deferred patch, extend the owned conditions:

```go
			patch.WithOwnedConditions{
				Conditions: []string{
					bootstrapv1beta1.DataSecretAvailableCondition,
					bootstrapv1beta1.ImageFactoryResolvedCondition,
				},
			},
```

Append to `controllers/image_factory.go`:

```go

// resolveInstallerImage resolves spec.imageFactory for config, records the result and the
// ImageFactoryResolved condition on it, and returns the installer image to render. A config
// without the block returns "" and carries neither status nor condition.
func (r *TalosConfigReconciler) resolveInstallerImage(ctx context.Context, config *bootstrapv1beta1.TalosConfig) (string, error) {
	if config.Spec.ImageFactory == nil {
		config.Status.ImageFactory = nil
		meta.RemoveStatusCondition(&config.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)

		return "", nil
	}

	if r.ImageFactory == nil {
		err := errors.New("spec.imageFactory is set but the controller has no Image Factory client; set --image-factory-url")
		conditions.Set(config, metav1.Condition{
			Type:    bootstrapv1beta1.ImageFactoryResolvedCondition,
			Status:  metav1.ConditionFalse,
			Reason:  bootstrapv1beta1.ImageFactoryUnavailableReason,
			Message: err.Error(),
		})

		return "", err
	}

	status, err := resolveImageFactory(ctx, r.ImageFactory, config.Spec, config.Status.ImageFactory)
	if err != nil {
		conditions.Set(config, metav1.Condition{
			Type:    bootstrapv1beta1.ImageFactoryResolvedCondition,
			Status:  metav1.ConditionFalse,
			Reason:  imageFactoryReason(err),
			Message: err.Error(),
		})

		return "", fmt.Errorf("resolving spec.imageFactory: %w", err)
	}

	config.Status.ImageFactory = status
	conditions.Set(config, metav1.Condition{
		Type:    bootstrapv1beta1.ImageFactoryResolvedCondition,
		Status:  metav1.ConditionTrue,
		Reason:  bootstrapv1beta1.ImageFactoryResolvedReason,
		Message: status.InstallerImage,
	})

	return status.InstallerImage, nil
}

// installerImageFromStatus returns the installer image the last resolution recorded, or "".
func installerImageFromStatus(config *bootstrapv1beta1.TalosConfig) string {
	if config.Status.ImageFactory == nil {
		return ""
	}

	return config.Status.ImageFactory.InstallerImage
}
```

Add to the imports of `controllers/image_factory.go`: `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`, `"k8s.io/apimachinery/pkg/api/meta"`, `"sigs.k8s.io/cluster-api/util/conditions"`.

- [ ] **Step 4: Use it in reconcileGenerate and the MachinePool gate; delete the infra hook**

In `controllers/talosconfig_controller.go` replace

```go
	// The infrastructure provider may have resolved an installer image for this machine, e.g.
	// from a Talos Image Factory schematic built out of its actual hardware. Apply it first so
	// an explicit strategic patch in the TalosConfig still takes precedence.
	installerImage, err := installerImageFor(ctx, r.Client, tcScope.ConfigOwner.Unstructured)
	if err != nil {
		return err
	}
```

with

```go
	// spec.imageFactory names the Image Factory schematic this machine installs and upgrades
	// with. Resolve it (registering the schematic and pinning the Talos version) and apply the
	// resulting installer image first, so an explicit strategic patch in the TalosConfig still
	// takes precedence.
	installerImage, err := r.resolveInstallerImage(ctx, config)
	if err != nil {
		return err
	}
```

In `controllers/machinepool_inplace.go` replace

```go
	// The installer image an infrastructure provider resolves is per-InfraMachine, and a pool has
	// no single one; installerImageFor also only knows how to read a Machine owner, so it resolves
	// to nothing here. The renderer therefore injects no image for a pool, and this hash has to be
	// computed the same way or it would never agree with what writeBootstrapData stamped.
	desiredHash, err := renderedConfigHash(scope, "")
```

with

```go
	// The installer image is resolved per TalosConfig from spec.imageFactory and recorded in
	// status, so a pool renders it like a Machine does. The gate hashes the recorded image: a
	// spec change that alters the schematic changes the spec hash on its own, and after the
	// re-render the recorded image matches what the renderer stamped.
	desiredHash, err := renderedConfigHash(scope, installerImageFromStatus(scope.Config))
```

In `controllers/installer_image.go` delete `InstallerImageStatusField`, `installerImageFor` and the imports they used (`context`, `unstructured`, `runtime`, `schema`, `types`, `client`, `capiv1`), leaving the package clause, the `fmt` import and `installImagePatch`. Update the doc comment of `installImagePatch` to say the image comes from `resolveInstallerImage`.

- [ ] **Step 5: Build and run the controller tests**

Run: `go build ./... && go test ./controllers/`
Expected: PASS for the new tests and the existing MachinePool tests. If `conditions.Set` is reported as undefined for the `metav1.Condition` form, the file already imports the v1beta2 helpers under the name `conditions` in `talosconfig_controller.go`; use the same import path in `image_factory.go`.

- [ ] **Step 6: Commit**

```bash
git add controllers
git commit -m "feat: render the installer image CABPT resolves from spec.imageFactory"
```

---

### Task 5: The in-place handler hashes the recorded installer image

**Files (CABPT):**
- Modify: `internal/inplace/updatemachine.go:300-345` (`installerImage`), `internal/inplace/updatemachine_test.go` (fixture and a new test)

**Interfaces:**
- Consumes: `bootstrapv1beta1.TalosConfig.Status.ImageFactory`.
- Produces: `(*Handler).installerImage(ctx, machine *clusterv1.Machine) (string, error)` reading the TalosConfig named by `machine.Spec.Bootstrap.ConfigRef`.

- [ ] **Step 1: Extend the fixture and write the failing test**

In `internal/inplace/updatemachine_test.go`:

- change the signature to `func newUpdateFixture(t *testing.T, runningVersion, configImage, secretHash, resolvedImage string) *updateFixture` and update the three existing calls to pass `""` as the last argument;
- register the bootstrap scheme: after `require.NoError(t, clusterv1.AddToScheme(scheme))` add `require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))`;
- add a TalosConfig object carrying the resolved image, and reference it from the machine:

```go
	liveConfig := &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "machine-1"},
		Spec:       bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"},
	}
	if resolvedImage != "" {
		liveConfig.Status.ImageFactory = &bootstrapv1beta1.ImageFactoryStatus{InstallerImage: resolvedImage}
	}
```

  include `liveConfig` in `WithObjects(secret, liveMachine, liveConfig)`, and set on the request machine:

```go
			Bootstrap: clusterv1.Bootstrap{
				DataSecretName: ptr.To("machine-1-bootstrap-data"),
				ConfigRef:      clusterv1.ContractVersionedObjectReference{APIGroup: bootstrapv1beta1.GroupVersion.Group, Kind: "TalosConfig", Name: "machine-1"},
			},
```

- add the tests:

```go
// The resolved installer image lives in TalosConfig status, which Cluster API strips from the
// hook request; the handler must read the live object or every regenerated secret looks stale.
func TestUpdateMachine_HashesTheRecordedInstallerImage(t *testing.T) {
	t.Parallel()

	const image = "factory.example.test/metal-installer/abc:v1.13.0"

	hash, err := bootstrapv1beta1.InPlaceConfigHash(
		bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"}, "1.34.0", image)
	require.NoError(t, err)

	f := newUpdateFixture(t, "v1.13.0", image, hash, image)

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Len(t, f.node.applied, 1, "a secret hashed with the recorded image is fresh")
	assert.Zero(t, resp.RetryAfterSeconds)
}

func TestUpdateMachine_WaitsWhenTheSecretPredatesTheImage(t *testing.T) {
	t.Parallel()

	const image = "factory.example.test/metal-installer/abc:v1.13.0"

	// Hashed without the image: what a secret rendered before spec.imageFactory was set carries.
	f := newUpdateFixture(t, "v1.13.0", image, freshHash(t), image)

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Positive(t, resp.RetryAfterSeconds, "must wait for CABPT to re-render with the image")
	assert.Empty(t, f.node.applied)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/inplace/ -run 'TestUpdateMachine_HashesTheRecordedInstallerImage'`
Expected: FAIL: the handler still reads the InfraMachine, finds no image, and reports the secret stale (RetryAfterSeconds positive, nothing applied).

- [ ] **Step 3: Replace installerImage**

In `internal/inplace/updatemachine.go` replace the whole `installerImage` method (and its doc comment) with:

```go
// installerImage reads the installer image CABPT resolved for the machine's TalosConfig.
//
// It is read from the live TalosConfig rather than taken from the hook request: Cluster API
// strips status before sending, and the image lives in status.imageFactory. Without it the
// hash recomputed here could not match the one CABPT stamped, and a genuinely fresh secret
// would be mistaken for a stale one.
func (h *Handler) installerImage(ctx context.Context, machine *clusterv1.Machine) (string, error) {
	ref := machine.Spec.Bootstrap.ConfigRef
	if !ref.IsDefined() {
		return "", nil
	}

	config := &bootstrapv1beta1.TalosConfig{}

	key := types.NamespacedName{Namespace: machine.Namespace, Name: ref.Name}
	if err := h.client.Get(ctx, key, config); err != nil {
		return "", fmt.Errorf("failed to read TalosConfig %s for the installer image: %w", key, err)
	}

	if config.Status.ImageFactory == nil {
		return "", nil
	}

	return config.Status.ImageFactory.InstallerImage, nil
}
```

Remove the now-unused imports `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"` and `"k8s.io/apimachinery/pkg/runtime/schema"` if nothing else in the file uses them (`go build` tells you).

- [ ] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/inplace/`
Expected: PASS, including the three pre-existing `TestUpdateMachine_*` tests, whose fixtures now also seed a TalosConfig without a resolved image.

- [ ] **Step 5: Commit**

```bash
git add internal/inplace
git commit -m "feat(inplace): hash the installer image recorded on the TalosConfig"
```

---

### Task 6: Flag, wiring and documentation

**Files (CABPT):**
- Modify: `main.go` (var block near `:40-58`, `InitFlags` `:65-100`, reconciler construction `:187-195`), `README.md:112-125`, `docs/in-place-updates.md:199-237`

- [ ] **Step 1: Add the flag and wire the client**

In `main.go` add `imageFactoryURL string` to the package-level `var (...)` block that holds `webhookPort`. In `InitFlags`, after the `enable-machine-pool-in-place-updates` flag:

```go
	fs.StringVar(&imageFactoryURL, "image-factory-url", imagefactory.DefaultURL,
		"Base URL of the Talos Image Factory used to register the schematic declared in a TalosConfig's "+
			"spec.imageFactory and to resolve its Talos version. The host of this URL becomes the registry in "+
			"the rendered machine.install.image.")
```

Before the `TalosConfigReconciler` is constructed:

```go
	factoryClient, err := imagefactory.NewClient(imageFactoryURL, nil)
	if err != nil {
		setupLog.Error(err, "invalid --image-factory-url")
		os.Exit(1)
	}
```

and add to the struct literal:

```go
		ImageFactory:              imagefactory.NewCached(factoryClient, 10*time.Minute),
```

Add imports `"time"` (if missing) and `"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/imagefactory"`.

Run: `go build ./... && go run . --help 2>&1 | grep -A2 image-factory-url`
Expected: the flag is listed with its default.

- [ ] **Step 2: Document the field**

In `README.md`, after the `hostname` bullet in "Fields available in the `TalosConfigTemplate` (and `TalosConfig`) resources", add:

```markdown
- `imageFactory` (optional): the [Talos Image Factory](https://factory.talos.dev) schematic the machine installs and upgrades with.
  CABPT registers the schematic, pins the Talos version (a bare minor in `talosVersion` resolves to the newest released patch once and is recorded in `status.imageFactory`), and renders `machine.install.image` as `<factory>/metal-installer/<schematic>:<version>`.
  The controller talks to the Factory at `--image-factory-url` (default `https://factory.talos.dev`). Fields:
  - `extensions`: official system extension names, e.g. `siderolabs/nvme-cli`; each must exist for the resolved version.
  - `extraKernelArgs`: kernel arguments baked into the Factory images.
  - `overlay`: `{name, image}` for single-board computers.
  - `bootloader`: `auto`, `dual-boot`, `grub` or `sd-boot`.
```

In `docs/in-place-updates.md`:

- replace the bullet `- **No installer image resolution.** ...` (two lines) with `- **Installer image.** \`machine.install.image\` comes from \`spec.imageFactory\` on the TalosConfig and is rendered and hashed for a pool exactly as for a Machine.`
- replace the whole `## Installer image resolution` section (from the heading to the end of the file) with:

```markdown
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
```

- [ ] **Step 3: Test, lint and commit**

```bash
go build ./... && go test ./api/... ./controllers/... ./internal/inplace/... ./internal/imagefactory/...
GOTOOLCHAIN=go1.26.1 make lint 2>&1 | tail -5
git add main.go README.md docs/in-place-updates.md
git commit -m "feat: configure the Image Factory URL and document spec.imageFactory"
```

Expected: all packages `ok`, lint clean.

---

### Task 7: Installer reference parsing in pkg/factory (actions)

**Files (actions, branch `schematic-from-config`):**
- Modify: `pkg/factory/client.go` (replace `ParseInstallerReference`), `pkg/factory/factory_test.go` (replace `TestParseInstallerReference`)

**Interfaces:**
- Produces: `factory.InstallerReference{Host, ID, Tag string}`, `factory.ParseInstallerReference(ref string) (InstallerReference, bool)`. Everything else in `pkg/factory` is untouched in this task; the schematic registration functions are deleted in Task 9 once nothing uses them.

- [ ] **Step 1: Replace the failing test**

In `pkg/factory/factory_test.go` replace the whole `TestParseInstallerReference` function with:

```go
func TestParseInstallerReference(t *testing.T) {
	tests := []struct {
		ref  string
		want InstallerReference
		ok   bool
	}{
		{"factory.talos.dev/metal-installer/" + testID + ":v1.14.1", InstallerReference{Host: "factory.talos.dev", ID: testID, Tag: "v1.14.1"}, true},
		{"factory.talos.dev/installer/" + testID + ":v1.14.1", InstallerReference{Host: "factory.talos.dev", ID: testID, Tag: "v1.14.1"}, true},
		{"factory.example.test:8443/metal-installer/" + testID, InstallerReference{Host: "factory.example.test:8443", ID: testID}, true},
		{"factory.talos.dev/metal-installer/" + testID + ":v1.14.1@sha256:abcd", InstallerReference{Host: "factory.talos.dev", ID: testID, Tag: "v1.14.1"}, true},
		{"ghcr.io/siderolabs/installer:v1.14.1", InstallerReference{}, false},
		{"factory.talos.dev/metal-installer/short:v1.14.1", InstallerReference{}, false},
		{"", InstallerReference{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseInstallerReference(tt.ref)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseInstallerReference(%q) = %+v, %v; want %+v, %v", tt.ref, got, ok, tt.want, tt.ok)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/appkins/src/sidero-community/actions && go test ./pkg/factory/ -run TestParseInstallerReference`
Expected: FAIL to compile, undefined `InstallerReference`.

- [ ] **Step 3: Replace the parser in client.go**

Replace the `installerReference` regexp and `ParseInstallerReference` at the end of `pkg/factory/client.go` with:

```go
var installerReference = regexp.MustCompile(`^([^/]+)/(?:metal-installer|installer)/([0-9a-f]{64})(?::([^@/]+))?(?:@.*)?$`)

// InstallerReference is a parsed Image Factory installer reference such as
// factory.talos.dev/metal-installer/<schematic id>:<version>.
type InstallerReference struct {
	// Host is the registry host, which is also the Factory's host.
	Host string
	// ID is the 64-hex-character schematic ID.
	ID string
	// Tag is the image tag, normally a Talos version; empty when the reference has none.
	Tag string
}

// ParseInstallerReference recognises Factory installer references and returns their parts.
func ParseInstallerReference(ref string) (InstallerReference, bool) {
	m := installerReference.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return InstallerReference{}, false
	}

	return InstallerReference{Host: m[1], ID: m[2], Tag: m[3]}, true
}
```

- [ ] **Step 4: Run the tests and commit**

Run: `go build ./... 2>&1 | head; go test ./pkg/factory/`

The build of `./talos2disk` fails at this point because `run.go` still calls the old two-value form; that is expected and fixed in Task 8. `pkg/factory` tests must PASS.

```bash
git add pkg/factory
git commit -m "feat(factory): parse host, id and tag of an installer reference"
```

---

### Task 8: talos2disk consumes the configured schematic

**Files (actions):**
- Modify: `pkg/hardware/object.go`, `pkg/hardware/object_test.go`, `talos2disk/inputs.go`, `talos2disk/settings.go`, `talos2disk/settings_test.go`, `talos2disk/deps.go`, `talos2disk/run.go`, `talos2disk/run_test.go`, `talos2disk/README.md`

**Interfaces:**
- Consumes: `factory.ParseInstallerReference`, `factory.NewClient`, `(*factory.Client).Versions/ImageURL`, `factory.ResolveVersion/IsFullVersion/IsBareMinor`, `factory.DefaultURL`; `talosconfig.Parse`, `(*Config).InstallDiskPath/ImageTag`; `disks.*`, `image.*`, `partition.*`, `cmdline.*`, `meta.*`, `talosnet.*`, `kargs.Merge`.
- Produces: `(*hardware.Hardware).OperatingSystemSlug() string`; package main `Inputs{Hardware, SchematicID, TalosVersion, DiskSelector, KernelArgs, FactoryURL, NetworkConfig, LinkNaming string; StripSignature bool; RetryWindow time.Duration; DryRun bool}`, `Settings{Disk DiskChoice; SchematicID, SchematicSource, Version, VersionSource, FactoryURL, FactoryURLSource, KernelArgs string; Network talosnet.Overrides}`, `resolveSettings(in, hw, cfg) (Settings, error)`, `Deps` without `Kube`, `Plan` without schematic/override fields, `run(ctx, in, deps) error`.

- [ ] **Step 1: Add the operating_system slug accessor**

Append to `pkg/hardware/object_test.go` `TestParseObject`, before its closing brace:

```go
	if hw.OperatingSystemSlug() != "abc" {
		t.Fatalf("OperatingSystemSlug() = %q", hw.OperatingSystemSlug())
	}
```

and in `TestParseObjectNilSafety` extend the condition with `|| hw.OperatingSystemSlug() != ""`.

Append to `pkg/hardware/object.go`:

```go
// OperatingSystemSlug returns metadata.instance.operating_system.slug, the schematic ID a
// resolver may have written onto the Hardware, or "".
func (h *Hardware) OperatingSystemSlug() string {
	if h.Spec.Metadata == nil || h.Spec.Metadata.Instance == nil || h.Spec.Metadata.Instance.OperatingSystem == nil {
		return ""
	}

	return strings.TrimSpace(h.Spec.Metadata.Instance.OperatingSystem.Slug)
}
```

Run: `go test ./pkg/hardware/` → PASS.

- [ ] **Step 2: Rewrite the settings test**

Overwrite `talos2disk/settings_test.go`:

```go
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sidero-community/actions/pkg/hardware"
	"github.com/sidero-community/actions/pkg/talosconfig"
)

const (
	cfgID = "2222222222222222222222222222222222222222222222222222222222222222"
	envID = "3333333333333333333333333333333333333333333333333333333333333333"
	hwID  = "4444444444444444444444444444444444444444444444444444444444444444"
)

func hw(annotations map[string]string, disks ...string) *hardware.Hardware {
	h := &hardware.Hardware{Metadata: hardware.ObjectMeta{Name: "node1", Namespace: "tinkerbell", Annotations: annotations}}
	for _, d := range disks {
		h.Spec.Disks = append(h.Spec.Disks, hardware.Disk{Device: d})
	}
	return h
}

func withOS(h *hardware.Hardware, slug, version string) *hardware.Hardware {
	h.Spec.Metadata = &hardware.Metadata{Instance: &hardware.Instance{OperatingSystem: &hardware.OperatingSystem{Slug: slug, Version: version}}}
	return h
}

func cfgWithImage(image string) *talosconfig.Config {
	return &talosconfig.Config{Found: true, Install: talosconfig.Install{Image: image}}
}

func TestSchematicAndFactoryChain(t *testing.T) {
	cfg := cfgWithImage("factory.example.test:8443/metal-installer/" + cfgID + ":v1.14.1")

	tests := []struct {
		name                     string
		in                       Inputs
		hw                       *hardware.Hardware
		cfg                      *talosconfig.Config
		wantID, wantIDSource     string
		wantURL, wantURLSource   string
		wantVersion, wantVSource string
	}{
		{"env wins", Inputs{SchematicID: envID, FactoryURL: "http://mirror:8080/"}, withOS(hw(nil), hwID, "v1.13.9"), cfg,
			envID, "SCHEMATIC_ID", "http://mirror:8080", "FACTORY_URL", "v1.14.1", "machine.install.image"},
		{"config reference", Inputs{}, withOS(hw(nil), hwID, "v1.13.9"), cfg,
			cfgID, "machine.install.image", "https://factory.example.test:8443", "machine.install.image", "v1.14.1", "machine.install.image"},
		{"hardware fallback", Inputs{}, withOS(hw(nil), hwID, "v1.13.9"), nil,
			hwID, "metadata.instance.operating_system.slug", "https://factory.talos.dev", "default", "v1.13.9", "metadata.instance.operating_system.version"},
		{"non-factory image keeps tag only", Inputs{}, withOS(hw(nil), hwID, "v1.13.9"), cfgWithImage("ghcr.io/siderolabs/installer:v1.14.0"),
			hwID, "metadata.instance.operating_system.slug", "https://factory.talos.dev", "default", "v1.14.0", "machine.install.image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := resolveSettings(tt.in, tt.hw, tt.cfg)
			if err != nil {
				t.Fatalf("resolveSettings() error: %v", err)
			}
			if s.SchematicID != tt.wantID || s.SchematicSource != tt.wantIDSource {
				t.Errorf("schematic = %q from %q", s.SchematicID, s.SchematicSource)
			}
			if s.FactoryURL != tt.wantURL || s.FactoryURLSource != tt.wantURLSource {
				t.Errorf("factory = %q from %q", s.FactoryURL, s.FactoryURLSource)
			}
			if s.Version != tt.wantVersion || s.VersionSource != tt.wantVSource {
				t.Errorf("version = %q from %q", s.Version, s.VersionSource)
			}
		})
	}

	if _, err := resolveSettings(Inputs{TalosVersion: "v1.14.1"}, hw(nil), nil); err == nil || !strings.Contains(err.Error(), "schematic") {
		t.Fatalf("expected a missing-schematic error, got %v", err)
	}
	if _, err := resolveSettings(Inputs{SchematicID: "not-hex"}, withOS(hw(nil), "", "v1.14.1"), nil); err == nil || !strings.Contains(err.Error(), "SCHEMATIC_ID") {
		t.Fatalf("expected a SCHEMATIC_ID format error, got %v", err)
	}
}

func TestDiskChain(t *testing.T) {
	base := withOS(hw(nil, "/dev/sda"), hwID, "v1.14.1")
	cfgSelector := &talosconfig.Config{Found: true, Install: talosconfig.Install{DiskSelector: []byte("type: nvme"), Disk: "/dev/sdz"}}
	cfgPath := &talosconfig.Config{Found: true, Install: talosconfig.Install{Disk: "/dev/sdz"}}
	cfgPlaceholder := &talosconfig.Config{Found: true, Install: talosconfig.Install{Disk: "DISK_ID"}}

	tests := []struct {
		name       string
		in         Inputs
		hw         *hardware.Hardware
		cfg        *talosconfig.Config
		wantSel    string
		wantPath   string
		wantSource string
	}{
		{"env wins", Inputs{DiskSelector: `{"model": "X*"}`}, base, cfgSelector, `{"model": "X*"}`, "", "DISK_SELECTOR"},
		{"config selector", Inputs{}, base, cfgSelector, "type: nvme", "", "machine.install.diskSelector"},
		{"config path", Inputs{}, base, cfgPath, "", "/dev/sdz", "machine.install.disk"},
		{"placeholder falls to hardware", Inputs{}, base, cfgPlaceholder, "", "/dev/sda", "spec.disks[0].device"},
		{"default", Inputs{}, withOS(hw(nil), hwID, "v1.14.1"), nil, `{ "size": ">= 100GB" }`, "", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := resolveSettings(tt.in, tt.hw, tt.cfg)
			if err != nil {
				t.Fatalf("resolveSettings() error: %v", err)
			}
			if s.Disk.Source != tt.wantSource || s.Disk.Path != tt.wantPath {
				t.Fatalf("disk = %+v", s.Disk)
			}
			if tt.wantSel != "" && (s.Disk.Selector == nil || s.Disk.Selector.String() != tt.wantSel) {
				t.Fatalf("selector = %v, want %q", s.Disk.Selector, tt.wantSel)
			}
		})
	}
}

func TestVersionChainAndKernelArgs(t *testing.T) {
	h := withOS(hw(map[string]string{hardware.AnnotationContract: "v1.13"}), hwID, "")
	cfg := &talosconfig.Config{
		Found:   true,
		Install: talosconfig.Install{ExtraKernelArgs: []string{"talos.logging.kernel=udp://1.2.3.4:514/", "net.ifnames=1"}},
		Network: talosconfig.Network{Hostname: "cfg-host", Nameservers: []string{"1.1.1.1"}},
		Time:    talosconfig.Time{Servers: []string{"time.example"}},
	}

	s, err := resolveSettings(Inputs{KernelArgs: "net.ifnames=0 talos.config=http://x/user-data"}, h, cfg)
	if err != nil {
		t.Fatalf("resolveSettings() error: %v", err)
	}
	if s.Version != "v1.13" || s.VersionSource != hardware.AnnotationContract {
		t.Errorf("version = %q from %q", s.Version, s.VersionSource)
	}
	if s.KernelArgs != "talos.logging.kernel=udp://1.2.3.4:514/ net.ifnames=0 talos.config=http://x/user-data" {
		t.Errorf("KernelArgs = %q", s.KernelArgs)
	}
	if s.Network.Hostname != "cfg-host" || strings.Join(s.Network.Nameservers, ",") != "1.1.1.1" || strings.Join(s.Network.TimeServers, ",") != "time.example" {
		t.Errorf("Network = %+v", s.Network)
	}

	if _, err := resolveSettings(Inputs{TalosVersion: "latest"}, h, cfg); err == nil {
		t.Fatal("expected an error for a malformed version")
	}
	if _, err := resolveSettings(Inputs{}, withOS(hw(nil), hwID, ""), nil); err == nil {
		t.Fatal("expected an error without any version")
	}
}

func TestInputsFromEnv(t *testing.T) {
	for _, key := range []string{"FACTORY_URL", "RETRY_DURATION_MINUTES", "LINK_NAMING", "SCHEMATIC_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("HARDWARE", "{}")
	t.Setenv("DRY_RUN", "true")
	in, err := inputsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if in.FactoryURL != "" || in.RetryWindow != 10*time.Minute || !in.DryRun {
		t.Fatalf("inputs = %+v", in)
	}

	t.Setenv("RETRY_DURATION_MINUTES", "soon")
	if _, err := inputsFromEnv(); err == nil {
		t.Fatal("expected an error for a non-numeric retry duration")
	}
	t.Setenv("RETRY_DURATION_MINUTES", "")

	t.Setenv("LINK_NAMING", "random")
	if _, err := inputsFromEnv(); err == nil {
		t.Fatal("expected an error for an unknown LINK_NAMING")
	}
}
```

- [ ] **Step 3: Rewrite inputs.go**

```go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Inputs are the action's environment variables, parsed.
type Inputs struct {
	Hardware       string
	SchematicID    string
	TalosVersion   string
	DiskSelector   string
	KernelArgs     string
	FactoryURL     string
	NetworkConfig  string
	LinkNaming     string
	StripSignature bool
	RetryWindow    time.Duration
	DryRun         bool
}

const defaultRetryMinutes = 10

func inputsFromEnv() (Inputs, error) {
	in := Inputs{
		Hardware:      os.Getenv("HARDWARE"),
		SchematicID:   strings.TrimSpace(os.Getenv("SCHEMATIC_ID")),
		TalosVersion:  strings.TrimSpace(os.Getenv("TALOS_VERSION")),
		DiskSelector:  strings.TrimSpace(os.Getenv("DISK_SELECTOR")),
		KernelArgs:    strings.TrimSpace(os.Getenv("KERNEL_ARGS")),
		FactoryURL:    strings.TrimSpace(os.Getenv("FACTORY_URL")),
		NetworkConfig: os.Getenv("NETWORK_CONFIG"),
		LinkNaming:    strings.TrimSpace(os.Getenv("LINK_NAMING")),
	}

	// Unparsable booleans mean false, as in the other actions.
	in.StripSignature, _ = strconv.ParseBool(os.Getenv("STRIP_SIGNATURE"))
	in.DryRun, _ = strconv.ParseBool(os.Getenv("DRY_RUN"))

	minutes := defaultRetryMinutes

	if raw := strings.TrimSpace(os.Getenv("RETRY_DURATION_MINUTES")); raw != "" {
		m, err := strconv.Atoi(raw)
		if err != nil || m < 0 {
			return Inputs{}, fmt.Errorf("RETRY_DURATION_MINUTES must be a non-negative integer, got %q", raw)
		}

		minutes = m
	}

	in.RetryWindow = time.Duration(minutes) * time.Minute

	switch in.LinkNaming {
	case "", "kernel", "predictable":
	default:
		return Inputs{}, fmt.Errorf("LINK_NAMING must be \"kernel\" or \"predictable\", got %q", in.LinkNaming)
	}

	return in, nil
}
```

- [ ] **Step 4: Rewrite settings.go**

```go
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sidero-community/actions/pkg/disks"
	"github.com/sidero-community/actions/pkg/factory"
	"github.com/sidero-community/actions/pkg/hardware"
	"github.com/sidero-community/actions/pkg/kargs"
	"github.com/sidero-community/actions/pkg/talosconfig"
	"github.com/sidero-community/actions/pkg/talosnet"
)

// DiskChoice is how the install disk is to be chosen: by selector or by path.
type DiskChoice struct {
	Selector *disks.Selector
	Path     string
	// Source names the layer the choice came from, for logging.
	Source string
}

// Settings are the resolved inputs of the install after the fall-through:
// environment, then the machine configuration, then the Hardware, then defaults.
type Settings struct {
	Disk             DiskChoice
	SchematicID      string
	SchematicSource  string
	Version          string
	VersionSource    string
	FactoryURL       string
	FactoryURLSource string
	// KernelArgs is the config's extraKernelArgs with KERNEL_ARGS merged on top.
	KernelArgs string
	Network    talosnet.Overrides
}

var schematicID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func resolveSettings(in Inputs, hw *hardware.Hardware, cfg *talosconfig.Config) (Settings, error) {
	var (
		s   Settings
		err error
	)

	if s.Disk, err = resolveDisk(in, hw, cfg); err != nil {
		return Settings{}, err
	}

	var ref factory.InstallerReference

	if cfg != nil {
		ref, _ = factory.ParseInstallerReference(cfg.Install.Image)
	}

	if s.SchematicID, s.SchematicSource, err = resolveSchematic(in, hw, ref); err != nil {
		return Settings{}, err
	}

	if s.Version, s.VersionSource, err = resolveVersion(in, hw, cfg); err != nil {
		return Settings{}, err
	}

	s.FactoryURL, s.FactoryURLSource = resolveFactoryURL(in, ref)

	var configArgs string
	if cfg != nil {
		configArgs = strings.Join(cfg.Install.ExtraKernelArgs, " ")
		s.Network = talosnet.Overrides{Hostname: cfg.Network.Hostname, Nameservers: cfg.Network.Nameservers, TimeServers: cfg.Time.Servers}
	}

	s.KernelArgs = kargs.Merge(configArgs, in.KernelArgs)

	return s, nil
}

func resolveDisk(in Inputs, hw *hardware.Hardware, cfg *talosconfig.Config) (DiskChoice, error) {
	switch {
	case in.DiskSelector != "":
		sel, err := disks.ParseSelector([]byte(in.DiskSelector))
		if err != nil {
			return DiskChoice{}, fmt.Errorf("DISK_SELECTOR: %w", err)
		}

		return DiskChoice{Selector: sel, Source: "DISK_SELECTOR"}, nil
	case cfg != nil && len(cfg.Install.DiskSelector) > 0:
		sel, err := disks.ParseSelector(cfg.Install.DiskSelector)
		if err != nil {
			return DiskChoice{}, fmt.Errorf("machine.install.diskSelector: %w", err)
		}

		return DiskChoice{Selector: sel, Source: "machine.install.diskSelector"}, nil
	case cfg.InstallDiskPath() != "":
		return DiskChoice{Path: cfg.InstallDiskPath(), Source: "machine.install.disk"}, nil
	case hw.FirstDisk() != "":
		return DiskChoice{Path: hw.FirstDisk(), Source: "spec.disks[0].device"}, nil
	default:
		return DiskChoice{Selector: disks.DefaultSelector(), Source: "default"}, nil
	}
}

// resolveSchematic picks the schematic ID: SCHEMATIC_ID, the Factory installer reference in
// machine.install.image, or the operating_system slug a resolver wrote on the Hardware.
func resolveSchematic(in Inputs, hw *hardware.Hardware, ref factory.InstallerReference) (string, string, error) {
	if in.SchematicID != "" {
		if !schematicID.MatchString(in.SchematicID) {
			return "", "", fmt.Errorf("SCHEMATIC_ID %q is not a 64-character hexadecimal schematic ID", in.SchematicID)
		}

		return in.SchematicID, "SCHEMATIC_ID", nil
	}

	if ref.ID != "" {
		return ref.ID, "machine.install.image", nil
	}

	if slug := hw.OperatingSystemSlug(); slug != "" {
		return slug, "metadata.instance.operating_system.slug", nil
	}

	return "", "", errors.New("no schematic: set SCHEMATIC_ID, or provide a Factory installer reference in machine.install.image, or metadata.instance.operating_system.slug on the Hardware")
}

func resolveVersion(in Inputs, hw *hardware.Hardware, cfg *talosconfig.Config) (string, string, error) {
	candidates := []struct{ value, source string }{
		{in.TalosVersion, "TALOS_VERSION"},
		{cfg.ImageTag(), "machine.install.image"},
		{hw.OperatingSystemVersion(), "metadata.instance.operating_system.version"},
		{hw.Annotation(hardware.AnnotationContract), hardware.AnnotationContract},
	}

	for _, c := range candidates {
		if c.value == "" {
			continue
		}

		if !factory.IsFullVersion(c.value) && !factory.IsBareMinor(c.value) {
			return "", "", fmt.Errorf("%s: %q is neither a full Talos version (v1.14.2) nor a bare minor (v1.14)", c.source, c.value)
		}

		return c.value, c.source, nil
	}

	return "", "", errors.New("no Talos version: set TALOS_VERSION, or provide machine.install.image, metadata.instance.operating_system.version or the talos.tinkerbell.org/contract annotation on the Hardware")
}

// resolveFactoryURL picks the Factory base URL: FACTORY_URL, the host of the installer
// reference over https, or the public Factory.
func resolveFactoryURL(in Inputs, ref factory.InstallerReference) (string, string) {
	if in.FactoryURL != "" {
		return strings.TrimRight(in.FactoryURL, "/"), "FACTORY_URL"
	}

	if ref.Host != "" {
		return "https://" + ref.Host, "machine.install.image"
	}

	return factory.DefaultURL, "default"
}
```

- [ ] **Step 5: Rewrite deps.go**

```go
package main

import (
	"log/slog"
	"net/http"

	"github.com/sidero-community/actions/pkg/cmdline"
	"github.com/sidero-community/actions/pkg/detect"
	"github.com/sidero-community/actions/pkg/disks"
	"github.com/sidero-community/actions/pkg/partition"
	"github.com/sidero-community/actions/pkg/talosnet"
)

// Deps are the environment-facing collaborators of the pipeline. Production
// values come from defaultDeps; tests substitute fakes.
type Deps struct {
	Logger     *slog.Logger
	HTTP       *http.Client
	SysRoot    string
	DevRoot    string
	Prober     disks.Prober
	Mounter    cmdline.Mounter
	MountPoint string
	Nodes      partition.NodeOptions
	Arch       string
	Namer      func(kernelNames bool) talosnet.Namer
}

func defaultDeps(logger *slog.Logger) Deps {
	return Deps{
		Logger:     logger,
		HTTP:       &http.Client{},
		SysRoot:    "/sys",
		DevRoot:    "/dev",
		Prober:     disks.BlockdeviceProber{},
		Mounter:    cmdline.VFAT{},
		MountPoint: cmdline.DefaultMountPoint,
		Nodes:      partition.NodeOptions{},
		Arch:       detect.Arch(),
		Namer: func(kernelNames bool) talosnet.Namer {
			return &talosnet.SysfsNamer{Predictable: !kernelNames, Logger: logger}
		},
	}
}
```

`detect.Arch()` is replaced by `runtime.GOARCH` in Task 9 when `pkg/detect` is deleted; leaving it here keeps this task compiling on its own.

- [ ] **Step 6: Rewrite run.go**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sidero-community/actions/pkg/cmdline"
	"github.com/sidero-community/actions/pkg/disks"
	"github.com/sidero-community/actions/pkg/factory"
	"github.com/sidero-community/actions/pkg/hardware"
	"github.com/sidero-community/actions/pkg/image"
	"github.com/sidero-community/actions/pkg/meta"
	"github.com/sidero-community/actions/pkg/partition"
	"github.com/sidero-community/actions/pkg/talosconfig"
	"github.com/sidero-community/actions/pkg/talosnet"
)

const (
	efiLabel   = "EFI"
	apiTimeout = 60 * time.Second
)

// Plan is everything decided before the first byte is written.
type Plan struct {
	Hardware    *hardware.Hardware
	Settings    Settings
	Disk        disks.Disk
	Candidates  []disks.Disk
	SchematicID string
	Version     string
	ImageURL    string
	KernelArgs  string
	KernelNames bool
	NetworkDoc  []byte
}

func run(ctx context.Context, in Inputs, deps Deps) error {
	logger := deps.Logger

	if strings.TrimSpace(in.Hardware) == "" {
		return errors.New("no Hardware object specified with environment variable [HARDWARE]")
	}

	hw, err := hardware.Parse([]byte(in.Hardware))
	if err != nil {
		return fmt.Errorf("HARDWARE: %w", err)
	}

	cfg, err := talosconfig.Parse(hw.UserData())
	if err != nil {
		return fmt.Errorf("spec.userData: %w", err)
	}

	settings, err := resolveSettings(in, hw, cfg)
	if err != nil {
		return err
	}

	if in.NetworkConfig != "" {
		if _, err := talosnet.Parse([]byte(in.NetworkConfig)); err != nil {
			return fmt.Errorf("NETWORK_CONFIG: %w", err)
		}
	}

	p, err := buildPlan(ctx, in, deps, hw, settings)
	if err != nil {
		return err
	}

	logPlan(logger, in, p)

	if in.DryRun {
		logger.Info("DRY_RUN is set; leaving the disk untouched")

		return nil
	}

	return execute(ctx, in, deps, p)
}

func buildPlan(ctx context.Context, in Inputs, deps Deps, hw *hardware.Hardware, settings Settings) (*Plan, error) {
	logger := deps.Logger
	p := &Plan{Hardware: hw, Settings: settings, SchematicID: settings.SchematicID}

	enum, err := disks.Enumerate(deps.SysRoot, deps.DevRoot, deps.Prober)
	if err != nil {
		return nil, err
	}

	for name, err := range enum.Skipped {
		logger.Warn("Skipping block device that could not be probed", "device", name, "error", err)
	}

	for _, d := range enum.Disks {
		logger.Info("Found disk", "device", d.DevPath, "size", d.Size, "transport", d.Transport, "rotational", d.Rotational,
			"readonly", d.Readonly, "cdrom", d.CDROM, "model", d.Model, "serial", d.Serial, "wwid", d.WWID, "busPath", d.BusPath, "eligible", disks.Eligible(d))
	}

	if settings.Disk.Path != "" {
		p.Disk, err = disks.SelectPath(enum.Disks, settings.Disk.Path)
		if err != nil {
			return nil, fmt.Errorf("install disk from %s: %w", settings.Disk.Source, err)
		}

		p.Candidates = []disks.Disk{p.Disk}
	} else {
		p.Disk, p.Candidates, err = disks.Select(enum.Disks, settings.Disk.Selector)
		if err != nil {
			return nil, fmt.Errorf("install disk from %s: %w", settings.Disk.Source, err)
		}
	}

	if len(p.Candidates) > 1 {
		logger.Warn("Several disks match the selector; taking the first by device name as Talos does",
			"selector", settings.Disk.Selector.String(), "candidates", diskPaths(p.Candidates), "chosen", p.Disk.DevPath)
	}

	warnUnknownDisk(logger, hw, p.Disk)

	client, err := factory.NewClient(settings.FactoryURL, deps.HTTP)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", settings.FactoryURLSource, err)
	}

	p.Version = settings.Version
	if factory.IsBareMinor(p.Version) {
		apiCtx, cancel := context.WithTimeout(ctx, apiTimeout)
		defer cancel()

		versions, err := client.Versions(apiCtx)
		if err != nil {
			return nil, err
		}

		p.Version, err = factory.ResolveVersion(p.Version, versions)
		if err != nil {
			return nil, err
		}
	}

	p.ImageURL = client.ImageURL(p.SchematicID, p.Version, deps.Arch)

	p.KernelArgs = settings.KernelArgs
	p.KernelNames = slices.Contains(strings.Fields(p.KernelArgs), "net.ifnames=0")

	switch in.LinkNaming {
	case "kernel":
		p.KernelNames = true
	case "predictable":
		p.KernelNames = false
	}

	switch {
	case in.NetworkConfig != "":
		p.NetworkDoc = []byte(in.NetworkConfig)
	case talosnet.HasStaticAddress(&hw.Spec):
		netCfg, err := talosnet.FromHardwareWithOverrides(&hw.Spec, deps.Namer(p.KernelNames), settings.Network)
		if err != nil {
			return nil, fmt.Errorf("building network configuration: %w", err)
		}

		p.NetworkDoc, err = netCfg.Marshal()
		if err != nil {
			return nil, err
		}
	default:
		logger.Warn("No interface has a static address and NETWORK_CONFIG is unset; META network configuration will be skipped")
	}

	return p, nil
}

func execute(ctx context.Context, in Inputs, deps Deps, p *Plan) error {
	logger := deps.Logger
	device := p.Disk.DevPath

	logger.Info("Writing image", "url", p.ImageURL, "device", device)

	err := image.Retry(ctx, in.RetryWindow, logger, func() error {
		_, err := image.Write(ctx, deps.HTTP, p.ImageURL, device, image.Options{Logger: logger})

		return err
	})
	if err != nil {
		return fmt.Errorf("writing image: %w", err)
	}

	efi, err := partition.Locate(device, efiLabel)
	if err != nil {
		return fmt.Errorf("after writing the image: %w", err)
	}

	if _, err := meta.Locate(device); err != nil {
		return fmt.Errorf("after writing the image: %w", err)
	}

	if p.KernelArgs != "" {
		node, err := partition.EnsureNode(device, efi.Index, deps.Nodes)
		if err != nil {
			return err
		}

		res, err := cmdline.Apply(node, p.KernelArgs, cmdline.Options{StripSignature: in.StripSignature, Mounter: deps.Mounter, MountPoint: deps.MountPoint, Logger: logger})
		if err != nil {
			return fmt.Errorf("setting kernel arguments: %w", err)
		}

		logger.Info("Kernel arguments applied", "updated", res.Updated, "unchanged", res.Unchanged)

		for name, lines := range res.Cmdlines {
			logger.Info("Command line", "uki", name, "cmdlines", lines)
		}
	}

	if len(p.NetworkDoc) > 0 {
		if err := meta.WriteTag(device, meta.MetalNetworkPlatformConfig, p.NetworkDoc); err != nil {
			return fmt.Errorf("writing network configuration to META: %w", err)
		}

		logger.Info("Wrote network configuration to META", "key", fmt.Sprintf("%#x", meta.MetalNetworkPlatformConfig), "bytes", len(p.NetworkDoc))
		logger.Info("Network configuration:\n" + string(p.NetworkDoc))
	}

	logger.Info("Talos installed", "device", device, "schematic", p.SchematicID, "version", p.Version, "image", p.ImageURL)

	return nil
}

// logPlan reports every decision. It never logs userData.
func logPlan(logger *slog.Logger, in Inputs, p *Plan) {
	s := p.Settings

	selector := ""
	if s.Disk.Selector != nil {
		selector = s.Disk.Selector.String()
	}

	linkNaming := "predictable"
	if p.KernelNames {
		linkNaming = "kernel"
	}

	logger.Info("Plan: install disk", "device", p.Disk.DevPath, "source", s.Disk.Source, "selector", selector, "path", s.Disk.Path, "candidates", diskPaths(p.Candidates))
	logger.Info("Plan: schematic", "id", p.SchematicID, "source", s.SchematicSource, "factory", s.FactoryURL, "factorySource", s.FactoryURLSource)
	logger.Info("Plan: version", "version", p.Version, "source", s.VersionSource, "image", p.ImageURL)
	logger.Info("Plan: kernel arguments", "args", p.KernelArgs, "linkNaming", linkNaming)
	logger.Info("Plan: META network configuration", "write", len(p.NetworkDoc) > 0, "verbatim", in.NetworkConfig != "")
}

func warnUnknownDisk(logger *slog.Logger, hw *hardware.Hardware, d disks.Disk) {
	declared, known := 0, false

	for _, hd := range hw.Spec.Disks {
		if hd.Device == "" {
			continue
		}

		declared++

		resolved := hd.Device
		if r, err := filepath.EvalSymlinks(hd.Device); err == nil {
			resolved = r
		}

		if resolved == d.DevPath || hd.Device == d.DevPath {
			known = true
		}
	}

	if declared > 0 && !known {
		logger.Warn("Selected disk is not listed in Hardware spec.disks", "device", d.DevPath)
	}

	if inv := hw.OutOfBand(); inv != nil && len(inv.BlockDevices) > 0 && d.Serial != "" {
		for _, b := range inv.BlockDevices {
			if b.SerialNumber == d.Serial {
				return
			}
		}

		logger.Warn("Selected disk serial is not in the out-of-band inventory", "device", d.DevPath, "serial", d.Serial)
	}
}

func diskPaths(list []disks.Disk) []string {
	out := make([]string, 0, len(list))
	for _, d := range list {
		out = append(out, d.DevPath)
	}

	return out
}
```

- [ ] **Step 7: Rewrite the pipeline test**

Overwrite `talos2disk/run_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/partition/gpt"
	"github.com/klauspost/compress/zstd"

	"github.com/sidero-community/actions/pkg/disks"
	"github.com/sidero-community/actions/pkg/meta"
	"github.com/sidero-community/actions/pkg/partition"
	"github.com/sidero-community/actions/pkg/talosnet"
	"github.com/sidero-community/actions/pkg/uki"
	"github.com/sidero-community/actions/pkg/uki/ukitest"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// talosLikeImage builds an 8 MiB GPT image with EFI and META partitions.
func talosLikeImage(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.raw")
	d, err := diskfs.Create(path, 8*1024*1024, diskfs.SectorSize512)
	if err != nil {
		t.Fatal(err)
	}
	table := &gpt.Table{
		Partitions: []*gpt.Partition{
			{Index: 1, Start: 2048, End: 4095, Type: gpt.EFISystemPartition, Name: "EFI"},
			{Index: 2, Start: 4096, End: 6143, Type: gpt.LinuxFilesystem, Name: "META"},
		},
		LogicalSectorSize: 512, PhysicalSectorSize: 512, ProtectiveMBR: true,
	}
	if err := d.Partition(table); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func zstdBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type recorder struct {
	mu        sync.Mutex
	posts     int
	imageGets []string
}

// newFactoryServer serves /versions and the raw image for any schematic ID at v1.14.1,
// and records every POST so tests can assert that the action registers nothing.
func newFactoryServer(t *testing.T, image []byte) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		switch {
		case r.Method == http.MethodPost:
			rec.posts++
			http.Error(w, "the action must not POST", http.StatusMethodNotAllowed)
		case r.Method == http.MethodGet && r.URL.Path == "/versions":
			_, _ = w.Write([]byte(`["v1.13.9","v1.14.0","v1.14.1"]`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/image/") && strings.HasSuffix(r.URL.Path, "/v1.14.1/metal-amd64.raw.zst"):
			rec.imageGets = append(rec.imageGets, r.URL.Path)
			_, _ = w.Write(image)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type fakeProber map[string]disks.Disk

func (f fakeProber) Probe(devPath string) (disks.Disk, error) {
	d, ok := f[filepath.Base(devPath)]
	if !ok {
		return disks.Disk{}, errors.New("no such device")
	}
	return d, nil
}

type dirMounter struct{ dir string }

func (m dirMounter) Mount(_, target string) error { return os.Symlink(m.dir, target) }
func (m dirMounter) Unmount(target string) error  { return os.Remove(target) }

type fixture struct {
	root, devRoot, sysRoot, efiDir, diskFile string
	deps                                     Deps
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{root: root, devRoot: filepath.Join(root, "dev"), sysRoot: filepath.Join(root, "sys"), efiDir: filepath.Join(root, "efi")}
	for _, dir := range []string{
		f.devRoot,
		filepath.Join(f.sysRoot, "block", "sda", "sda1"),
		filepath.Join(f.sysRoot, "block", "loop0"),
		filepath.Join(f.efiDir, "EFI", "Linux"),
		filepath.Join(root, "nodes"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.diskFile = filepath.Join(f.devRoot, "sda")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(f.diskFile, nil, 0o600))
	must(os.WriteFile(filepath.Join(f.sysRoot, "block", "sda", "sda1", "dev"), []byte("8:1\n"), 0o644))
	must(ukitest.Write(filepath.Join(f.efiDir, "EFI", "Linux", "Talos-v1.14.1.efi"), ukitest.TalosLike("talos.platform=metal console=ttyS0"), ukitest.Options{}))

	f.deps = Deps{
		Logger:     quietLogger(),
		HTTP:       http.DefaultClient,
		SysRoot:    f.sysRoot,
		DevRoot:    f.devRoot,
		Prober:     fakeProber{"sda": {Size: 500_000_000_000, Transport: "sata", Model: "TestDisk", Serial: "S1"}, "loop0": {Size: 1 << 40}},
		Mounter:    dirMounter{dir: f.efiDir},
		MountPoint: filepath.Join(root, "mnt"),
		Nodes: partition.NodeOptions{
			SysRoot: f.sysRoot, Wait: 10 * time.Millisecond, Poll: time.Millisecond, PrivateDir: filepath.Join(root, "nodes"),
			Mknod: func(path string, _, _ uint32) error { return os.WriteFile(path, nil, 0o600) },
		},
		Arch:  "amd64",
		Namer: func(kernelNames bool) talosnet.Namer { return &talosnet.SysfsNamer{Root: f.sysRoot, Predictable: !kernelNames} },
	}
	return f
}

const machineConfig = `version: v1alpha1
machine:
  type: worker
  install:
    disk: DISK_ID
    image: factory.talos.dev/metal-installer/` + cfgID + `:v1.14.1
    extraKernelArgs:
      - talos.logging.kernel=udp://10.0.0.5:514/
  network:
    hostname: node1
cluster:
  clusterName: demo
`

func (f *fixture) hardwareJSON(t *testing.T, userData string, osSlug string) string {
	t.Helper()
	instance := map[string]any{"hostname": "inst"}
	if osSlug != "" {
		instance["operating_system"] = map[string]any{"slug": osSlug, "version": "v1.14"}
	}
	hw := map[string]any{
		"metadata": map[string]any{"name": "node1", "namespace": "tinkerbell"},
		"spec": map[string]any{
			"disks": []map[string]string{{"device": f.diskFile}},
			"interfaces": []map[string]any{{"dhcp": map[string]any{
				"mac": "52:54:00:12:34:01", "iface_name": "eth0", "hostname": "dhcp-name",
				"ip": map[string]any{"address": "10.0.80.10", "netmask": "255.255.255.0", "gateway": "10.0.80.1", "family": 4},
			}}},
			"metadata": map[string]any{"instance": instance},
		},
		"status": map[string]any{"attributes": map[string]any{"outOfBand": map[string]any{
			"blockDevices": []map[string]any{{"serialNumber": "S1", "model": "TestDisk"}},
		}}},
	}
	if userData != "" {
		hw["spec"].(map[string]any)["userData"] = userData
	}
	raw, err := json.Marshal(hw)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRunInstallsFromTheConfiguredSchematic(t *testing.T) {
	f := newFixture(t)
	image := talosLikeImage(t)
	srv, rec := newFactoryServer(t, zstdBytes(t, image))

	in := Inputs{
		Hardware:    f.hardwareJSON(t, machineConfig, ""),
		KernelArgs:  "net.ifnames=0 talos.config=http://10.0.0.1:7080/2009-04-04/user-data",
		FactoryURL:  srv.URL,
		RetryWindow: 0,
	}
	if err := run(context.Background(), in, f.deps); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	if rec.posts != 0 {
		t.Fatalf("the action must not register schematics, saw %d POSTs", rec.posts)
	}
	if len(rec.imageGets) != 1 || rec.imageGets[0] != "/image/"+cfgID+"/v1.14.1/metal-amd64.raw.zst" {
		t.Fatalf("image fetched from %v, want the configured schematic", rec.imageGets)
	}

	got, err := os.ReadFile(f.diskFile)
	if err != nil {
		t.Fatal(err)
	}
	// The META partition (sectors 4096-6143) is rewritten by the network
	// configuration step; everything else must match the image byte for byte.
	const metaStart, metaEnd = 4096 * 512, 6144 * 512
	if len(got) != len(image) || !bytes.Equal(got[:metaStart], image[:metaStart]) || !bytes.Equal(got[metaEnd:], image[metaEnd:]) {
		t.Fatal("disk content outside META differs from the Factory image")
	}

	info, err := uki.Inspect(filepath.Join(f.efiDir, "EFI", "Linux", "Talos-v1.14.1.efi"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Cmdlines[0] != "talos.platform=metal console=ttyS0 talos.logging.kernel=udp://10.0.0.5:514/ net.ifnames=0 talos.config=http://10.0.0.1:7080/2009-04-04/user-data" {
		t.Errorf("cmdline = %q", info.Cmdlines[0])
	}

	doc, ok, err := meta.ReadTag(f.diskFile, meta.MetalNetworkPlatformConfig)
	if err != nil || !ok {
		t.Fatalf("META tag: ok=%v err=%v", ok, err)
	}
	for _, want := range []string{"hostname: node1", "address: 10.0.80.10/24", "linkName: eth0", "gateway: 10.0.80.1"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("META document lacks %q:\n%s", want, doc)
		}
	}
}

func TestRunSchematicIDEnvOverridesTheConfig(t *testing.T) {
	f := newFixture(t)
	srv, rec := newFactoryServer(t, zstdBytes(t, talosLikeImage(t)))

	in := Inputs{Hardware: f.hardwareJSON(t, machineConfig, ""), SchematicID: envID, FactoryURL: srv.URL}
	if err := run(context.Background(), in, f.deps); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if len(rec.imageGets) != 1 || !strings.Contains(rec.imageGets[0], envID) {
		t.Fatalf("image fetched from %v, want SCHEMATIC_ID", rec.imageGets)
	}
}

func TestRunFallsBackToHardwareOperatingSystem(t *testing.T) {
	f := newFixture(t)
	srv, rec := newFactoryServer(t, zstdBytes(t, talosLikeImage(t)))

	in := Inputs{Hardware: f.hardwareJSON(t, "", hwID), FactoryURL: srv.URL}
	if err := run(context.Background(), in, f.deps); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if len(rec.imageGets) != 1 || rec.imageGets[0] != "/image/"+hwID+"/v1.14.1/metal-amd64.raw.zst" {
		t.Fatalf("image fetched from %v, want the Hardware slug with the bare minor resolved to v1.14.1", rec.imageGets)
	}
}

func TestRunDryRunTouchesNothing(t *testing.T) {
	f := newFixture(t)
	srv, rec := newFactoryServer(t, zstdBytes(t, talosLikeImage(t)))

	in := Inputs{Hardware: f.hardwareJSON(t, machineConfig, ""), KernelArgs: "net.ifnames=0", FactoryURL: srv.URL, DryRun: true}
	if err := run(context.Background(), in, f.deps); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if st, err := os.Stat(f.diskFile); err != nil || st.Size() != 0 {
		t.Fatalf("disk must stay empty, size=%d err=%v", st.Size(), err)
	}
	if len(rec.imageGets) != 0 || rec.posts != 0 {
		t.Fatalf("dry run fetched %v and posted %d times", rec.imageGets, rec.posts)
	}
}

func TestRunFailsBeforeWriteOnBadInput(t *testing.T) {
	f := newFixture(t)
	if err := run(context.Background(), Inputs{}, f.deps); err == nil || !strings.Contains(err.Error(), "HARDWARE") {
		t.Fatalf("expected a HARDWARE error, got %v", err)
	}
	if err := run(context.Background(), Inputs{Hardware: f.hardwareJSON(t, "machine: [broken", "")}, f.deps); err == nil || !strings.Contains(err.Error(), "userData") {
		t.Fatalf("expected a userData error, got %v", err)
	}
	if err := run(context.Background(), Inputs{Hardware: f.hardwareJSON(t, "", ""), TalosVersion: "v1.14.1"}, f.deps); err == nil || !strings.Contains(err.Error(), "schematic") {
		t.Fatalf("expected a missing-schematic error, got %v", err)
	}
	if err := run(context.Background(), Inputs{Hardware: f.hardwareJSON(t, "", hwID), TalosVersion: "v1.14.1", DiskSelector: `{"model": "nothing*"}`, FactoryURL: "http://127.0.0.1:9"}, f.deps); err == nil || !strings.Contains(err.Error(), "no disk matches") {
		t.Fatalf("expected a disk selection error before any network call, got %v", err)
	}
	if st, _ := os.Stat(f.diskFile); st.Size() != 0 {
		t.Fatal("disk must be untouched after early failures")
	}
}
```

- [ ] **Step 8: Run the tests**

Run: `go test -race ./talos2disk/ ./pkg/hardware/`
Expected: PASS.

- [ ] **Step 9: Update the README**

In `talos2disk/README.md`:

- In the intro paragraph replace "determines and registers the Factory schematic, resolves the Talos version" with "reads the Factory schematic and Talos version that CABPT rendered into `machine.install.image`" and delete "and records the resulting installer image and machine configuration overrides on the Hardware object".
- In the example, remove the `volumes:` block with `/var/lib/tink/shared:/shared` and the `KUBECONFIG` line.
- In the environment table, remove the `EXTENSIONS`, `OVERLAY`, `NVIDIA_EXTENSIONS` and `KUBECONFIG` rows; add after `HARDWARE`:

```markdown
| `SCHEMATIC_ID` | no | | Factory schematic ID (64 hex characters). Overrides the reference in the machine configuration and the Hardware. |
```

  and change the `FACTORY_URL` row's default to "derived" with the description "Image Factory base URL. Defaults to `https://` plus the host of the installer reference in `machine.install.image`, else `https://factory.talos.dev`."
- In "How settings are chosen", replace the `Extensions` row with:

```markdown
| Schematic ID | `SCHEMATIC_ID`; the ID in `machine.install.image` (`<host>/metal-installer/<id>:<tag>`); `metadata.instance.operating_system.slug`. |
| Factory URL | `FACTORY_URL`; `https://<host of machine.install.image>`; `https://factory.talos.dev`. |
```

- Delete the whole "What is written back" section and its heading.
- In "Verifying", drop the `kubectl` line.

- [ ] **Step 10: Commit**

```bash
git add pkg/hardware talos2disk
git commit -m "feat(talos2disk): install the schematic named by machine.install.image"
```

---

### Task 9: Remove detection, write-back and schematic registration from the actions repo

**Files (actions):**
- Delete: `pkg/detect/`, `pkg/kube/`, `pkg/talosconfig/patch.go`, `pkg/talosconfig/patch_test.go`, `pkg/factory/schematic.go`
- Modify: `pkg/factory/client.go` (remove `Registration`, `CreateSchematic`, `GetSchematic`, `InstallerImage`), `pkg/factory/factory_test.go`, `talos2disk/deps.go` (`runtime.GOARCH`), `go.mod`, `go.sum`, `README.md` (root table wording), `docs/superpowers/plans/2026-09-12-talos2disk.md` (add a header note)

- [ ] **Step 1: Delete the packages and functions**

```bash
cd /home/appkins/src/sidero-community/actions
git rm -r -q pkg/detect pkg/kube pkg/talosconfig/patch.go pkg/talosconfig/patch_test.go pkg/factory/schematic.go
```

In `pkg/factory/client.go` delete the `Registration` type, `CreateSchematic`, `GetSchematic` and `InstallerImage` methods and the `bytes` import if it becomes unused. In `pkg/factory/factory_test.go` delete `TestBuildDedupesSortsAndMarshals`, `TestParseOverlay`, `TestParseSchematicAndMissingExtensions`, the `POST /schematics` and `/schematics/<id>` cases in `newFactoryServer`, and in `TestClientCreateGetVersionsAndURLs` keep only the `Versions` and `ImageURL` assertions (rename it `TestClientVersionsAndImageURL`); remove the `io` import if unused.

In `talos2disk/deps.go` replace `detect.Arch()` with `runtime.GOARCH`, swap the `detect` import for `"runtime"`.

- [ ] **Step 2: Tidy, build, test**

```bash
go mod tidy
go build ./... && go vet ./...
go test -race ./...
grep -c "k8s.io" go.mod || echo "no k8s deps left"
```

Expected: build and vet clean, every package `ok`, and `go.mod` has no `k8s.io` requirement.

- [ ] **Step 3: Documentation**

- Root `README.md`, talos2disk row: "Install Talos from the Image Factory: select the disk, write the image for the schematic named in the machine configuration, stamp kernel arguments, write network configuration to META".
- Prepend to `docs/superpowers/plans/2026-09-12-talos2disk.md`, after the header block: `> Superseded on 2026-09-12 by the CABPT-owned schematic revision; see the spec's revision note and the plan in cluster-api-bootstrap-provider-talos docs/superpowers/plans/2026-09-12-image-factory-schematic.md.`

- [ ] **Step 4: Format, lint, commit**

```bash
env -u GOBIN make formatters
env -u GOBIN make lint 2>&1 | tail -5
go test -race ./...
git add -A
git commit -m "refactor: drop hardware detection, schematic registration and the Hardware write-back"
```

Expected: `0 issues.` from golangci-lint and every package `ok`.

---

### Task 10: Chart Template without kubeconfig and the resolver yield revert

**Files (runtime-extensions, new branch `schematic-from-config` off `bmc-manager`):**
- Modify: `charts/cluster-api-runtime-extensions-tinkerbell/files/talos-install-template.yaml`, `charts/cluster-api-runtime-extensions-tinkerbell/Chart.yaml`, `docs/runtime-extensions-migration.md`
- Revert: commit `896f8ff` (touches `internal/resolve/resolve.go`, `reconciler.go`, `reconciler_test.go`)

- [ ] **Step 1: Branch and revert the resolver yield**

```bash
cd /home/appkins/src/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell
test -z "$(git status --short)"
git switch -c schematic-from-config
git revert --no-edit 896f8ff
go test ./internal/resolve/ -count=1
```

Expected: a clean revert commit and `ok`.

- [ ] **Step 2: Edit the Template**

In `files/talos-install-template.yaml`:

- In the leading comment, replace the sentence "writes the network configuration to META and records the installer image and configuration overrides on the Hardware through KUBECONFIG." with "and writes the network configuration to META. The schematic and version come from machine.install.image, which CABPT renders from the TalosConfig's imageFactory block."
- Delete the two lines `        volumes:` and `          - /var/lib/tink/shared:/shared` of the "Install Talos" action (keep the task-level `volumes:` untouched).
- Delete the comment lines starting "# Kubeconfig provisioned on the OSIE host" and "# hardware.tinkerbell.org. Without it the write-back is skipped." and the line `          KUBECONFIG: /shared/kubeconfig`.

Verify: `grep -c "KUBECONFIG\|/shared" charts/cluster-api-runtime-extensions-tinkerbell/files/talos-install-template.yaml` prints `0`.

- [ ] **Step 3: Bump the chart and update the migration note**

`Chart.yaml`: `version: 0.3.0` → `version: 0.3.1`.

In `docs/runtime-extensions-migration.md`, in the paragraph beginning `> **2026-09-12 update — one install action.**`, replace the text from "registers its own Factory schematic from machine" through "the owner annotation is present (see the talos2disk design spec in sidero-community/actions)." with:

```
> installs the schematic and version named by `machine.install.image`, which CABPT renders from the
> TalosConfig `imageFactory` block (see `docs/design/2026-09-12-image-factory-schematic.md` in
> cluster-api-bootstrap-provider-talos), streams the raw image, stamps `KERNEL_ARGS` and writes META.
> The action never talks to the Kubernetes API. The Template no longer reads `operating_system`; the
> Workflow gate remains as the guarantee that the Hardware carries a version fallback.
```

- [ ] **Step 4: Lint, render and commit**

```bash
helm lint charts/cluster-api-runtime-extensions-tinkerbell --set resolver.tinkerbellIP=10.0.0.1
helm template t charts/cluster-api-runtime-extensions-tinkerbell --set resolver.tinkerbellIP=10.0.0.1 --show-only templates/talos-install-template.yaml | grep -c "KUBECONFIG" || echo "no kubeconfig in rendered template"
git add charts docs/runtime-extensions-migration.md
git commit -m "feat(chart): talos2disk installs the schematic CABPT renders; drop the kubeconfig"
```

---

### Task 11: Revert the CAPT userData yield

**Files (CAPT, new branch `revert-userdata-yield` off `remove-talos-schematic`):**
- Revert: commit `6bc8da8` (`controller/machine/hardware.go`, `controller/machine/hardware_test.go`)

- [ ] **Step 1: Branch, revert, test**

```bash
cd /home/appkins/src/tinkerbell-community/cluster-api-provider-tinkerbell
test -z "$(git status --short)"
git switch -c revert-userdata-yield
git revert --no-edit 6bc8da8
GOTOOLCHAIN=go1.26.1 go test ./controller/machine/ -count=1
grep -c "UserDataOwnerAnnotation" controller/machine/hardware.go || echo "yield removed"
```

Expected: a clean revert commit, `ok`, and `yield removed`.

---

## Verification after all tasks

- CABPT: `go build ./... && go test ./api/... ./controllers/... ./internal/inplace/... ./internal/imagefactory/...` and `GOTOOLCHAIN=go1.26.1 make lint`; `git status` clean; `config/crd/bases` regenerated and committed.
- actions: `go test -race ./...`, `env -u GOBIN make lint`, `make talos2disk`.
- runtime-extensions: `go test ./internal/resolve/`, `helm lint`.
- CAPT: `go test ./controller/machine/`.
- Manual, before switching a live Template: create a TalosConfigTemplate with an `imageFactory` block, confirm `status.imageFactory.installerImage` and the `ImageFactoryResolved` condition on a generated TalosConfig, confirm the rendered bootstrap secret carries the same `machine.install.image`, then run a `DRY_RUN` talos2disk Workflow and read its plan lines.
