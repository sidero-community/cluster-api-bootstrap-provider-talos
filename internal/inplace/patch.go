// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package inplace implements the Cluster API in-place update Runtime Extension for Talos.
package inplace

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

// fieldPath is a dot-separated path into an object, e.g. "spec.bootOptions.isoURL".
type fieldPath string

func (p fieldPath) segments() []string {
	return strings.Split(string(p), ".")
}

// mergePatchForPaths builds a JSON merge patch that moves the listed paths of current to
// their values in desired, and nothing else.
//
// This is how the extension declares what it can absorb in-place. Cluster API applies the
// returned patch to the current object and compares the result against desired: if they
// match, the update proceeds in-place; if any field still differs, it falls back to a
// rolling replacement. Restricting the patch to an explicit path list therefore means the
// extension declines every field it does not name, simply by leaving it alone.
//
// Paths that are absent from desired but present in current are set to null, so that
// clearing a field is expressed correctly rather than being silently skipped.
//
// Returns an undefined Patch when the named paths already agree, which is the correct
// "nothing to do" answer rather than an error.
func mergePatchForPaths(current, desired map[string]any, paths []fieldPath) (runtimehooksv1.Patch, error) {
	patch := map[string]any{}
	changed := false

	for _, path := range paths {
		segments := path.segments()

		desiredValue, desiredFound, err := unstructured.NestedFieldNoCopy(desired, segments...)
		if err != nil {
			return runtimehooksv1.Patch{}, fmt.Errorf("failed to read desired %s: %w", path, err)
		}

		currentValue, currentFound, err := unstructured.NestedFieldNoCopy(current, segments...)
		if err != nil {
			return runtimehooksv1.Patch{}, fmt.Errorf("failed to read current %s: %w", path, err)
		}

		switch {
		case !desiredFound && !currentFound:
			// Neither side sets it; nothing to express.
			continue
		case desiredFound && currentFound && reflect.DeepEqual(desiredValue, currentValue):
			// Already in agreement; keep the patch minimal.
			continue
		case !desiredFound:
			// Present in current, absent in desired: an explicit null clears it. Without this
			// the field would keep its old value and the comparison would fail, needlessly
			// forcing a rolling replacement.
			if err := setNested(patch, nil, segments); err != nil {
				return runtimehooksv1.Patch{}, err
			}
		default:
			if err := setNested(patch, desiredValue, segments); err != nil {
				return runtimehooksv1.Patch{}, err
			}
		}

		changed = true
	}

	if !changed {
		return runtimehooksv1.Patch{}, nil
	}

	encoded, err := json.Marshal(patch)
	if err != nil {
		return runtimehooksv1.Patch{}, fmt.Errorf("failed to encode merge patch: %w", err)
	}

	return runtimehooksv1.Patch{
		PatchType: runtimehooksv1.JSONMergePatchType,
		Patch:     encoded,
	}, nil
}

// setNested writes value at the given path, creating intermediate maps as needed.
//
// unstructured.SetNestedField is not usable here: it deep-copies through a type switch that
// rejects arbitrary values, and it cannot store an explicit nil.
func setNested(obj map[string]any, value any, segments []string) error {
	cursor := obj

	for _, segment := range segments[:len(segments)-1] {
		next, ok := cursor[segment]
		if !ok {
			child := map[string]any{}
			cursor[segment] = child
			cursor = child

			continue
		}

		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("cannot set %s: %q is not an object", strings.Join(segments, "."), segment)
		}

		cursor = child
	}

	cursor[segments[len(segments)-1]] = value

	return nil
}

// prefixPaths returns paths rebased under prefix, e.g. for template objects where the
// interesting fields live under spec.template.spec rather than spec.
func prefixPaths(prefix string, paths []fieldPath) []fieldPath {
	out := make([]fieldPath, 0, len(paths))

	for _, path := range paths {
		out = append(out, fieldPath(prefix+"."+strings.TrimPrefix(string(path), "spec.")))
	}

	return out
}

// toUnstructured renders any object into a generic map so it can be compared and patched
// by field path without the extension needing to know its Go type. Infra machines arrive
// as raw JSON precisely so that the extension stays independent of infrastructure
// providers, and handling everything uniformly keeps one code path.
func toUnstructured(obj any) (map[string]any, error) {
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode object: %w", err)
	}

	out := map[string]any{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("failed to decode object: %w", err)
	}

	return out, nil
}
