// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"context"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/cluster-api/util/topology"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (r *TalosConfigTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithValidator(r).
		Complete()
}

//+kubebuilder:webhook:verbs=update,path=/validate-bootstrap-cluster-x-k8s-io-v1beta1-talosconfigtemplate,mutating=false,failurePolicy=fail,groups=bootstrap.cluster.x-k8s.io,resources=talosconfigtemplates,versions=v1beta1,name=vtalosconfigtemplate.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

var _ admission.Validator[*TalosConfigTemplate] = &TalosConfigTemplate{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type
func (r *TalosConfigTemplate) ValidateCreate(ctx context.Context, obj *TalosConfigTemplate) (admission.Warnings, error) {
	r = obj

	return nil, r.validate()
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type
func (r *TalosConfigTemplate) ValidateUpdate(ctx context.Context, oldObj *TalosConfigTemplate, newObj *TalosConfigTemplate) (admission.Warnings, error) {
	old := oldObj
	r = newObj

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Only the template spec is frozen: it is baked into every TalosConfig generated from this
	// template, so changing it would leave existing machines describing a configuration that no
	// longer exists. Template metadata is merely copied onto configs generated from now on, so it
	// stays mutable, matching KubeadmConfigTemplate and the infrastructure providers.
	//
	// Skip the check entirely if the request is a dry-run issued by the topology controller (#257).
	if !topology.IsDryRunRequest(req, r) && !cmp.Equal(r.Spec.Template.Spec, old.Spec.Template.Spec) {
		return nil, apierrors.NewBadRequest("TalosConfigTemplate spec.template.spec is immutable")
	}

	return nil, r.validate()
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type
func (r *TalosConfigTemplate) ValidateDelete(ctx context.Context, obj *TalosConfigTemplate) (admission.Warnings, error) {
	return nil, nil
}

func (r *TalosConfigTemplate) validate() error {
	allErrs := validateImageFactory(field.NewPath("spec", "template", "spec", "imageFactory"), r.Spec.Template.Spec.ImageFactory)
	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: GroupVersion.Group, Kind: "TalosConfigTemplate"},
		r.Name, allErrs)
}
