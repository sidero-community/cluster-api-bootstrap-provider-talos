// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package installerimage

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func machineFor(infraName string) *capiv1.Machine {
	m := &capiv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "capi"}}
	if infraName != "" {
		m.Spec.InfrastructureRef = capiv1.ContractVersionedObjectReference{
			APIGroup: "infrastructure.cluster.x-k8s.io", Kind: "TinkerbellMachine", Name: infraName,
		}
	}
	return m
}

func hardware(name, ownerName, ownerNamespace, annotation string) *unstructured.Unstructured {
	hw := &unstructured.Unstructured{}
	hw.SetAPIVersion("tinkerbell.org/v1alpha1")
	hw.SetKind("Hardware")
	hw.SetName(name)
	hw.SetNamespace("tinkerbell")
	hw.SetLabels(map[string]string{
		"v1alpha1.tinkerbell.org/ownerName":      ownerName,
		"v1alpha1.tinkerbell.org/ownerNamespace": ownerNamespace,
	})
	if annotation != "" {
		hw.SetAnnotations(map[string]string{AnnotationKey: annotation})
	}
	return hw
}

func infraMachine(name, statusImage string) *unstructured.Unstructured {
	infra := &unstructured.Unstructured{}
	infra.SetAPIVersion("infrastructure.cluster.x-k8s.io/v1beta2")
	infra.SetKind("TinkerbellMachine")
	infra.SetName(name)
	infra.SetNamespace("capi")
	if statusImage != "" {
		_ = unstructured.SetNestedField(infra.Object, statusImage, "status", "installerImage")
	}
	return infra
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := capiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		machine *capiv1.Machine
		objects []*unstructured.Unstructured
		want    string
	}{
		{
			name:    "hardware annotation wins over infra status",
			machine: machineFor("tm-1"),
			objects: []*unstructured.Unstructured{
				hardware("hw-1", "tm-1", "capi", "factory.talos.dev/metal-installer/resolver:v1.13.9"),
				infraMachine("tm-1", "factory.talos.dev/metal-installer/capt:v1.13.9"),
			},
			want: "factory.talos.dev/metal-installer/resolver:v1.13.9",
		},
		{
			name:    "status fallback while hardware is unannotated",
			machine: machineFor("tm-1"),
			objects: []*unstructured.Unstructured{
				hardware("hw-1", "tm-1", "capi", ""),
				infraMachine("tm-1", "factory.talos.dev/metal-installer/capt:v1.13.9"),
			},
			want: "factory.talos.dev/metal-installer/capt:v1.13.9",
		},
		{
			name:    "another machine's hardware is not consulted",
			machine: machineFor("tm-1"),
			objects: []*unstructured.Unstructured{
				hardware("hw-other", "tm-2", "capi", "factory.talos.dev/metal-installer/wrong:v1.13.9"),
				infraMachine("tm-1", ""),
			},
			want: "",
		},
		{
			name:    "owner namespace must match",
			machine: machineFor("tm-1"),
			objects: []*unstructured.Unstructured{
				hardware("hw-1", "tm-1", "other-ns", "factory.talos.dev/metal-installer/wrong:v1.13.9"),
			},
			want: "",
		},
		{
			name:    "nothing resolvable yields empty, not an error",
			machine: machineFor("tm-1"),
			want:    "",
		},
		{
			name:    "no infrastructure ref yields empty",
			machine: machineFor(""),
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(testScheme(t))
			for _, o := range tt.objects {
				builder = builder.WithObjects(o)
			}
			c := builder.Build()

			got, err := Resolve(context.Background(), c, tt.machine)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
}
