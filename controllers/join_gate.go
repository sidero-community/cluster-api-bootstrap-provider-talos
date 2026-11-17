// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

const (
	// JoinPolicyName names the ValidatingAdmissionPolicy and its binding in a workload cluster.
	JoinPolicyName = "talos-join.bootstrap.cluster.x-k8s.io"

	// JoinAllowlistName names the ConfigMap in kube-system the policy reads: a key id.<join ID>
	// for every living owner that restricts joining, and a key name.<node> for the Node of
	// every living Machine that does not.
	JoinAllowlistName = "talos-join-allowlist"

	// JoinTokenLabel marks the bootstrap token Secrets the gate manages in kube-system. Its
	// value is the join ID the token belongs to.
	JoinTokenLabel = "bootstrap.cluster.x-k8s.io/talos-join-token"

	// JoinEnforcementAnnotation on the allowlist says whether the policy refuses ("Deny") or
	// only warns ("Warn"), and JoinUncoveredAnnotation names the machines that keep it at Warn.
	JoinEnforcementAnnotation = "bootstrap.cluster.x-k8s.io/talos-join-enforcement"
	JoinUncoveredAnnotation   = "bootstrap.cluster.x-k8s.io/talos-join-uncovered"

	joinEnforcementDeny = "Deny"
	joinEnforcementWarn = "Warn"

	joinAllowlistIDPrefix   = "id."
	joinAllowlistNamePrefix = "name."

	joinGateNamespace = metav1.NamespaceSystem

	// joinGateMarkerSuffix names the Secret that records, next to the Cluster, that a gate was
	// installed in its workload cluster. Without one a cluster that never asked for the gate
	// is never connected to.
	joinGateMarkerSuffix = "-talos-join-gate"

	// joinGateResync repairs the workload cluster's side should something there remove it. The
	// policy refuses every kubelet while its allowlist is missing.
	joinGateResync = 5 * time.Minute

	// joinPolicyExpression admits a Node whose join ID is listed, or whose name is.
	joinPolicyExpression = "has(params.data) && (" +
		"(has(object.metadata.labels) && '" + JoinIDLabel + "' in object.metadata.labels && " +
		"('" + joinAllowlistIDPrefix + "' + object.metadata.labels['" + JoinIDLabel + "']) in params.data)" +
		" || ('" + joinAllowlistNamePrefix + "' + object.metadata.name) in params.data)"

	joinPolicyMessage = "'Node ' + object.metadata.name + ' does not belong to a living Cluster API Machine of this cluster'"

	// joinPolicyKubelets limits the policy to kubelets registering themselves.
	joinPolicyKubelets = "request.userInfo.groups.exists(g, g == 'system:nodes')"
)

// WorkloadClientFunc returns a client for the workload cluster of a Cluster.
type WorkloadClientFunc func(ctx context.Context, cluster client.ObjectKey) (client.Client, error)

// JoinGateReconciler keeps the workload cluster's side of spec.restrictJoin in step with the
// Machines of a Cluster: the allowlist of join IDs, the admission policy that reads it, and
// the bootstrap tokens of the workers that may still join.
type JoinGateReconciler struct {
	client.Client
	Log              logr.Logger
	WatchFilterValue string

	// WorkloadClient connects to a workload cluster. When nil the Cluster's kubeconfig Secret
	// is used.
	WorkloadClient WorkloadClientFunc
}

// joinGate is what a workload cluster should hold for a Cluster.
type joinGate struct {
	// restricted says that at least one TalosConfig of the cluster sets spec.restrictJoin.
	// Without one the cluster has no gate.
	restricted bool
	// allowlist is the data of the allowlist ConfigMap.
	allowlist map[string]string
	// tokens are the bootstrap tokens to keep, by token ID.
	tokens map[string]joinGateToken
	// uncovered are living machines the allowlist cannot describe: no join ID and no Node yet.
	// While there are any, refusing registrations would lock them out.
	uncovered []string
}

type joinGateToken struct {
	joinID string
	secret string
	owner  string
}

func (g *joinGate) enforcement() string {
	if len(g.uncovered) > 0 {
		return joinEnforcementWarn
	}

	return joinEnforcementDeny
}

func (r *JoinGateReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	clusterOf := func(namespace, name string) []ctrl.Request {
		if name == "" {
			return nil
		}

		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}}
	}

	b := ctrl.NewControllerManagedBy(mgr).
		Named("talos-join-gate").
		For(&capiv1.Cluster{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(mgr.GetScheme(), ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Watches(&capiv1.Machine{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
			return clusterOf(o.GetNamespace(), o.(*capiv1.Machine).Spec.ClusterName) //nolint:forcetypeassert // the watch is typed
		})).
		Watches(&bootstrapv1beta1.TalosConfig{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
			return clusterOf(o.GetNamespace(), o.GetLabels()[capiv1.ClusterNameLabel])
		}))

	if feature.Gates.Enabled(feature.MachinePool) {
		b = b.Watches(&capiv1.MachinePool{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
			return clusterOf(o.GetNamespace(), o.(*capiv1.MachinePool).Spec.ClusterName) //nolint:forcetypeassert // the watch is typed
		}))
	}

	if err := b.Complete(r); err != nil {
		return fmt.Errorf("failed setting up with a controller manager: %w", err)
	}

	return nil
}

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machines;machinepools,verbs=get;list;watch
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=talosconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *JoinGateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("cluster", req.NamespacedName)

	cluster := &capiv1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A cluster being deleted takes its gate with it, and the marker goes with the Cluster.
	if !cluster.DeletionTimestamp.IsZero() || annotations.IsPaused(cluster, cluster) {
		return ctrl.Result{}, nil
	}

	gate, err := r.desiredGate(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	marker := &corev1.Secret{}
	markerKey := client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name + joinGateMarkerSuffix}

	installed := true

	if err := r.Get(ctx, markerKey, marker); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		installed = false
	}

	if !gate.restricted && !installed {
		return ctrl.Result{}, nil
	}

	// Nodes register freely until there is an API server to hold a policy; the Cluster changing
	// brings the reconcile back.
	if !ptr.Deref(cluster.Status.Initialization.ControlPlaneInitialized, false) {
		return ctrl.Result{}, nil
	}

	workload, err := r.workloadClient(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("connecting to the workload cluster: %w", err)
	}

	if !gate.restricted {
		if err := removeJoinGate(ctx, workload); err != nil {
			return ctrl.Result{}, err
		}

		log.Info("removed the join gate, no TalosConfig restricts joining any more")

		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, marker))
	}

	// The marker comes first: once anything is in the workload cluster, removing it again must
	// not depend on remembering that.
	if !installed {
		if err := r.Create(ctx, joinGateMarker(cluster, markerKey)); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}

	if err := applyJoinGate(ctx, workload, gate); err != nil {
		return ctrl.Result{}, err
	}

	log.V(2).Info("reconciled the join gate", "enforcement", gate.enforcement(), "allowed", len(gate.allowlist), "tokens", len(gate.tokens), "uncovered", gate.uncovered)

	return ctrl.Result{RequeueAfter: joinGateResync}, nil
}

func (r *JoinGateReconciler) workloadClient(ctx context.Context, cluster client.ObjectKey) (client.Client, error) {
	if r.WorkloadClient != nil {
		return r.WorkloadClient(ctx, cluster)
	}

	return remote.NewClusterClient(ctx, "cluster-api-bootstrap-provider-talos", r.Client, cluster)
}

func joinGateMarker(cluster *capiv1.Cluster, key client.ObjectKey) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: key.Namespace,
			Name:      key.Name,
			Labels:    map[string]string{capiv1.ClusterNameLabel: cluster.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: capiv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       cluster.Name,
				UID:        cluster.UID,
			}},
		},
	}
}

// desiredGate works out the gate of a cluster from its Machines, MachinePools and their
// TalosConfigs.
func (r *JoinGateReconciler) desiredGate(ctx context.Context, cluster *capiv1.Cluster) (*joinGate, error) {
	gate := &joinGate{allowlist: map[string]string{}, tokens: map[string]joinGateToken{}}

	selectors := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{capiv1.ClusterNameLabel: cluster.Name},
	}

	machines := &capiv1.MachineList{}
	if err := r.List(ctx, machines, selectors...); err != nil {
		return nil, err
	}

	// The tokens derive from the cluster's secrets, which are only read when a worker needs one.
	var bundle *secrets.Bundle

	for i := range machines.Items {
		machine := &machines.Items[i]

		if !machine.DeletionTimestamp.IsZero() {
			continue
		}

		owner := "Machine " + machine.Name

		config, err := r.talosConfig(ctx, machine.Namespace, machine.Spec.Bootstrap.ConfigRef)
		if err != nil {
			return nil, err
		}

		if config == nil || !config.Spec.RestrictJoin {
			// A machine that does not register with a join ID is known by its Node, once it has one.
			if node := machine.Status.NodeRef.Name; node != "" && isAllowlistKey(joinAllowlistNamePrefix+node) {
				gate.allowlist[joinAllowlistNamePrefix+node] = owner
			} else {
				gate.uncovered = append(gate.uncovered, owner)
			}

			continue
		}

		gate.restricted = true
		gate.allowlist[joinAllowlistIDPrefix+string(machine.UID)] = owner

		// A worker's token is there for as long as it may have to ask for a certificate: until
		// its Node is ready, and again whenever it stops being so.
		if !hasJoinToken(config, false) || conditions.IsTrue(machine, capiv1.MachineNodeReadyCondition) {
			continue
		}

		if bundle == nil {
			if bundle, err = r.secretsBundle(ctx, cluster); err != nil {
				return nil, err
			}
		}

		id, secret, err := joinToken(bundle, string(machine.UID))
		if err != nil {
			return nil, err
		}

		gate.tokens[id] = joinGateToken{joinID: string(machine.UID), secret: secret, owner: owner}
	}

	if feature.Gates.Enabled(feature.MachinePool) {
		pools := &capiv1.MachinePoolList{}
		if err := r.List(ctx, pools, selectors...); err != nil {
			return nil, err
		}

		for i := range pools.Items {
			pool := &pools.Items[i]

			if !pool.DeletionTimestamp.IsZero() {
				continue
			}

			owner := "MachinePool " + pool.Name

			config, err := r.talosConfig(ctx, pool.Namespace, pool.Spec.Template.Spec.Bootstrap.ConfigRef)
			if err != nil {
				return nil, err
			}

			if config != nil && config.Spec.RestrictJoin {
				gate.restricted = true
				gate.allowlist[joinAllowlistIDPrefix+string(pool.UID)] = owner

				continue
			}

			// A pool may add a member at any time, and nothing tells it apart before it joins.
			gate.uncovered = append(gate.uncovered, owner)

			for _, node := range pool.Status.NodeRefs {
				if isAllowlistKey(joinAllowlistNamePrefix + node.Name) {
					gate.allowlist[joinAllowlistNamePrefix+node.Name] = owner
				}
			}
		}
	}

	slices.Sort(gate.uncovered)

	return gate, nil
}

func isAllowlistKey(key string) bool {
	return len(validation.IsConfigMapKey(key)) == 0
}

// talosConfig returns the TalosConfig a bootstrap reference names, or nil when it names
// something else or the TalosConfig is gone.
func (r *JoinGateReconciler) talosConfig(ctx context.Context, namespace string, ref capiv1.ContractVersionedObjectReference) (*bootstrapv1beta1.TalosConfig, error) {
	if !ref.IsDefined() || ref.GroupKind() != bootstrapv1beta1.GroupVersion.WithKind("TalosConfig").GroupKind() {
		return nil, nil
	}

	config := &bootstrapv1beta1.TalosConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, config); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return config, nil
}

// secretsBundle reads the cluster's secrets, which join tokens derive from.
func (r *JoinGateReconciler) secretsBundle(ctx context.Context, cluster *capiv1.Cluster) (*secrets.Bundle, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name + "-talos"}, secret); err != nil {
		return nil, fmt.Errorf("reading the cluster secrets to derive join tokens: %w", err)
	}

	return decodeSecretsBundle(secret)
}

// applyJoinGate writes gate to the workload cluster. The allowlist goes first, so the policy
// never reads one older than itself.
func applyJoinGate(ctx context.Context, workload client.Client, gate *joinGate) error {
	allowlist := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: joinGateNamespace, Name: JoinAllowlistName}}

	if _, err := controllerutil.CreateOrUpdate(ctx, workload, allowlist, func() error {
		if allowlist.Annotations == nil {
			allowlist.Annotations = map[string]string{}
		}

		allowlist.Annotations[JoinEnforcementAnnotation] = gate.enforcement()
		allowlist.Annotations[JoinUncoveredAnnotation] = strings.Join(gate.uncovered, ", ")
		allowlist.Data = maps.Clone(gate.allowlist)

		return nil
	}); err != nil {
		return fmt.Errorf("writing the join allowlist: %w", err)
	}

	if err := applyJoinTokens(ctx, workload, gate.tokens); err != nil {
		return err
	}

	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}}

	if _, err := controllerutil.CreateOrUpdate(ctx, workload, policy, func() error {
		policy.Spec = joinPolicySpec()

		return nil
	}); err != nil {
		return fmt.Errorf("writing the join policy: %w", err)
	}

	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}}

	if _, err := controllerutil.CreateOrUpdate(ctx, workload, binding, func() error {
		binding.Spec = joinPolicyBindingSpec(gate.enforcement())

		return nil
	}); err != nil {
		return fmt.Errorf("writing the join policy binding: %w", err)
	}

	return nil
}

func joinPolicySpec() admissionregistrationv1.ValidatingAdmissionPolicySpec {
	return admissionregistrationv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: ptr.To(admissionregistrationv1.Fail),
		ParamKind:     &admissionregistrationv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"},
		MatchConstraints: &admissionregistrationv1.MatchResources{
			ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionregistrationv1.RuleWithOperations{
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{""},
						APIVersions: []string{"v1"},
						Resources:   []string{"nodes"},
					},
				},
			}},
		},
		MatchConditions: []admissionregistrationv1.MatchCondition{{
			Name:       "kubelet-registration",
			Expression: joinPolicyKubelets,
		}},
		Validations: []admissionregistrationv1.Validation{{
			Expression:        joinPolicyExpression,
			MessageExpression: joinPolicyMessage,
			Reason:            ptr.To(metav1.StatusReasonForbidden),
		}},
	}
}

func joinPolicyBindingSpec(enforcement string) admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec {
	actions := []admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn, admissionregistrationv1.Audit}
	if enforcement == joinEnforcementDeny {
		actions = []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}
	}

	return admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName: JoinPolicyName,
		ParamRef: &admissionregistrationv1.ParamRef{
			Name:      JoinAllowlistName,
			Namespace: joinGateNamespace,
			// A missing allowlist admits nobody: whoever can delete it could as well edit it.
			ParameterNotFoundAction: ptr.To(admissionregistrationv1.DenyAction),
		},
		ValidationActions: actions,
	}
}

// applyJoinTokens makes the gate's bootstrap token Secrets exactly tokens.
func applyJoinTokens(ctx context.Context, workload client.Client, tokens map[string]joinGateToken) error {
	existing := &corev1.SecretList{}
	if err := workload.List(ctx, existing, client.InNamespace(joinGateNamespace), client.HasLabels{JoinTokenLabel}); err != nil {
		return fmt.Errorf("listing join tokens: %w", err)
	}

	for i := range existing.Items {
		secret := &existing.Items[i]

		if _, wanted := tokens[strings.TrimPrefix(secret.Name, joinTokenSecretPrefix)]; wanted {
			continue
		}

		if err := workload.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("removing join token %s: %w", secret.Name, err)
		}
	}

	for id, token := range tokens {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: joinGateNamespace, Name: joinTokenSecretPrefix + id}}

		if _, err := controllerutil.CreateOrUpdate(ctx, workload, secret, func() error {
			if secret.Labels == nil {
				secret.Labels = map[string]string{}
			}

			secret.Labels[JoinTokenLabel] = token.joinID
			secret.Type = corev1.SecretTypeBootstrapToken
			// The same Secret Talos writes for the cluster-wide token, for one machine.
			secret.Data = map[string][]byte{
				"token-id":                       []byte(id),
				"token-secret":                   []byte(token.secret),
				"usage-bootstrap-authentication": []byte("true"),
				"auth-extra-groups":              []byte("system:bootstrappers:nodes"),
				"description":                    []byte("Join token of " + token.owner + ", managed by cluster-api-bootstrap-provider-talos"),
			}

			return nil
		}); err != nil {
			return fmt.Errorf("writing the join token of %s: %w", token.owner, err)
		}
	}

	return nil
}

const joinTokenSecretPrefix = "bootstrap-token-"

// removeJoinGate takes the gate out of a workload cluster, the binding first so that nothing
// is refused on the way.
func removeJoinGate(ctx context.Context, workload client.Client) error {
	for _, object := range []client.Object{
		&admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}},
		&admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: joinGateNamespace, Name: JoinAllowlistName}},
	} {
		if err := workload.Delete(ctx, object); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("removing the join gate: %w", err)
		}
	}

	return applyJoinTokens(ctx, workload, nil)
}
