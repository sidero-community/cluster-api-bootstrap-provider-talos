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
