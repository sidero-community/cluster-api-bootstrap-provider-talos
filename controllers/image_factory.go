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

	return bootstrapv1beta1.ImageFactoryUnavailableReason
}

// imageFactoryInputs hashes everything that determines the resolved image: the requested
// version and the schematic block. Encoding is JSON of a fixed-order struct, so it is stable.
func imageFactoryInputs(spec bootstrapv1beta1.TalosConfigSpec) string {
	encoded, _ := json.Marshal(struct { //nolint:errchkjson // the struct holds only strings and slices
		TalosVersion string                             `json:"talosVersion"`
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
		return "", &imageFactoryError{
			Reason: bootstrapv1beta1.ImageFactoryInvalidSpecReason,
			Err:    fmt.Errorf("talosVersion %q must be a full version (v1.14.2) or a minor (v1.14) to build an installer image", raw),
		}
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
		return "", &imageFactoryError{
			Reason: bootstrapv1beta1.ImageFactoryNoReleasedPatchReason,
			Err:    fmt.Errorf("the Image Factory serves no released patch of %s", raw),
		}
	}

	return best, nil
}

// resolveImageFactory turns spec.imageFactory into the status block holding the installer
// image. It returns (nil, nil) when the spec has no block, reuses current while its
// observedInputs match the spec, and otherwise resolves against the Factory.
func resolveImageFactory(ctx context.Context, api imagefactory.API, spec bootstrapv1beta1.TalosConfigSpec, current *bootstrapv1beta1.ImageFactoryStatus) (*bootstrapv1beta1.ImageFactoryStatus, error) {
	if spec.ImageFactory == nil {
		return nil, nil //nolint:nilnil // no block means nothing to resolve, by contract
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
				return nil, &imageFactoryError{
					Reason: bootstrapv1beta1.ImageFactoryUnknownExtensionReason,
					Err:    fmt.Errorf("extension %q is not offered by the Image Factory for Talos %s", name, version),
				}
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
