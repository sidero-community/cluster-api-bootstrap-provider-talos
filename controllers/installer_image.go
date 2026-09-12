// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import "fmt"

// installImagePatch renders a strategic merge patch pinning the Talos installer image that
// resolveInstallerImage derived from spec.imageFactory.
//
// It is applied ahead of any user-supplied strategic patches so that an explicit patch in the
// TalosConfig still wins. The resolved image is a good default, not an override of intent.
func installImagePatch(image string) string {
	return fmt.Sprintf("machine:\n  install:\n    image: %s\n", image)
}
