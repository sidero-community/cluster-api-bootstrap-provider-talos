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
	f.extensions["v1.14.3"] = []string{"siderolabs/nvme-cli", "siderolabs/intel-ucode"}
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
