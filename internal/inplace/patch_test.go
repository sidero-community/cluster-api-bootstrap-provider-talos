// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

// Inside an allowlisted object the patch names only what changed: a removed key as null,
// a changed or added value as its desired value, and nothing that already agrees. Lists
// are replaced whole, as a merge patch cannot address their items.
func TestMergePatchForPaths_DiffsObjects(t *testing.T) {
	t.Parallel()

	current := map[string]any{"spec": map[string]any{
		"bootOptions": map[string]any{"bootMode": "isoboot", "isoURL": "http://a/x.iso"},
		"references": map[string]any{
			"keep": map[string]any{"name": "a", "namespace": "capi"},
			"edit": map[string]any{"name": "b", "namespace": "capi"},
			"gone": map[string]any{"name": "c"},
		},
		"tasks":           []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
		"addressFromPool": map[string]any{"apiGroup": "ipam.cluster.x-k8s.io", "name": "p1"},
	}}
	desired := map[string]any{"spec": map[string]any{
		"bootOptions": map[string]any{"bootMode": "netboot"},
		"references": map[string]any{
			"keep":  map[string]any{"name": "a", "namespace": "capi"},
			"edit":  map[string]any{"name": "b"},
			"added": map[string]any{"name": "d"},
		},
		"tasks":           []any{map[string]any{"name": "a"}},
		"addressFromPool": map[string]any{"name": "p1"},
	}}

	patch, err := mergePatchForPaths(current, desired, []fieldPath{"spec.bootOptions", "spec.references", "spec.tasks"})
	require.NoError(t, err)
	require.Equal(t, runtimehooksv1.JSONMergePatchType, patch.PatchType)

	assert.JSONEq(t, `{"spec": {
		"bootOptions": {"bootMode": "netboot", "isoURL": null},
		"references": {"edit": {"namespace": null}, "added": {"name": "d"}, "gone": null},
		"tasks": [{"name": "a"}]
	}}`, string(patch.Patch))
}
