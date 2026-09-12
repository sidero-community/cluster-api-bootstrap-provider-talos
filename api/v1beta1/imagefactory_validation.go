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
