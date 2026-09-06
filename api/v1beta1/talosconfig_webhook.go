// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/topology"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (r *TalosConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithValidator(r).
		Complete()
}

//+kubebuilder:webhook:verbs=create;update,path=/validate-bootstrap-cluster-x-k8s-io-v1beta1-talosconfig,mutating=false,failurePolicy=fail,groups=bootstrap.cluster.x-k8s.io,resources=talosconfigs,versions=v1beta1,name=vtalosconfig.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

var _ admission.Validator[*TalosConfig] = &TalosConfig{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type
func (r *TalosConfig) ValidateCreate(ctx context.Context, obj *TalosConfig) (admission.Warnings, error) {
	r = obj
	return nil, r.validate()
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type
func (r *TalosConfig) ValidateUpdate(ctx context.Context, oldObj *TalosConfig, newObj *TalosConfig) (admission.Warnings, error) {
	old := oldObj
	r = newObj

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Immutability only binds user-driven updates. A spec change is admitted when:
	//   - the request is a dry-run issued by the topology controller (#257);
	//   - an in-place update is under way, which requires the owning controller to write the
	//     desired bootstrap config onto the existing object. The owner stamps
	//     clusterv1.UpdateInProgressAnnotation and the desired spec in the same admission request
	//     (mirroring the KubeadmControlPlane triggerInPlaceUpdate flow), so checking the incoming
	//     object is sufficient here;
	//   - the object is topology-owned. For a MachinePool the topology controller creates the
	//     TalosConfig directly rather than rotating a template, and reconciles ClusterClass
	//     changes onto it with a real (non dry-run) apply.
	specManagedByController := topology.IsDryRunRequest(req, r) || IsInPlaceUpdate(r) || isTopologyOwned(r)

	if !specManagedByController && !cmp.Equal(r.Spec, old.Spec) {
		return nil, apierrors.NewBadRequest("TalosConfig.Spec is immutable")
	}

	return nil, r.validate()
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type
func (r *TalosConfig) ValidateDelete(ctx context.Context, obj *TalosConfig) (admission.Warnings, error) {
	return nil, nil
}

// isTopologyOwned reports whether an object is managed as part of a Cluster topology, i.e.
// whether it carries clusterv1.ClusterTopologyOwnedLabel.
func isTopologyOwned(obj metav1.Object) bool {
	_, ok := obj.GetLabels()[clusterv1.ClusterTopologyOwnedLabel]

	return ok
}

func (r *TalosConfig) validate() error {
	var allErrs field.ErrorList

	switch r.Spec.Hostname.Source {
	case "":
	case HostnameSourceMachineName:
	case HostnameSourceInfrastructureName:
	default:
		allErrs = append(allErrs,
			field.Invalid(field.NewPath("spec").Child("hostname").Child("source"), r.Spec.Hostname.Source,
				fmt.Sprintf("valid values are: %q", []HostnameSource{HostnameSourceMachineName, HostnameSourceInfrastructureName}),
			),
		)
	}

	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: GroupVersion.Group, Kind: "TalosConfig"},
		r.Name, allErrs)
}
