/*
Copyright 2021 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ingress

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"knative.dev/net-gateway-api/pkg/reconciler/ingress/config"
	"knative.dev/net-gateway-api/pkg/reconciler/ingress/resources"
	netv1alpha1 "knative.dev/networking/pkg/apis/networking/v1alpha1"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/logging"
)

// reconcileHTTPRoute reconciles HTTPRoute.
func (c *Reconciler) reconcileHTTPRoute(
	ctx context.Context, ing *netv1alpha1.Ingress,
	rule *netv1alpha1.IngressRule,
	tlsGw *gatewayv1alpha2.Gateway,
) (*gatewayv1alpha2.HTTPRoute, error) {
	recorder := controller.GetEventRecorder(ctx)

	httproute, err := c.httprouteLister.HTTPRoutes(ing.Namespace).Get(resources.LongestHost(rule.Hosts))
	if apierrs.IsNotFound(err) {
		desired, err := resources.MakeHTTPRoute(ctx, ing, rule, tlsGw)
		if err != nil {
			return nil, err
		}
		httproute, err = c.gwapiclient.GatewayV1alpha2().HTTPRoutes(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			recorder.Eventf(ing, corev1.EventTypeWarning, "CreationFailed", "Failed to create HTTPRoute: %v", err)
			return nil, fmt.Errorf("failed to create HTTPRoute: %w", err)
		}

		recorder.Eventf(ing, corev1.EventTypeNormal, "Created", "Created HTTPRoute %q", httproute.GetName())
		return httproute, nil
	} else if err != nil {
		return nil, err
	} else {
		desired, err := resources.MakeHTTPRoute(ctx, ing, rule, tlsGw)
		if err != nil {
			return nil, err
		}

		if !equality.Semantic.DeepEqual(httproute.Spec, desired.Spec) ||
			!equality.Semantic.DeepEqual(httproute.Annotations, desired.Annotations) ||
			!equality.Semantic.DeepEqual(httproute.Labels, desired.Labels) {

			// Don't modify the informers copy.
			origin := httproute.DeepCopy()
			origin.Spec = desired.Spec
			origin.Annotations = desired.Annotations
			origin.Labels = desired.Labels

			updated, err := c.gwapiclient.GatewayV1alpha2().HTTPRoutes(origin.Namespace).Update(
				ctx, origin, metav1.UpdateOptions{})
			if err != nil {
				recorder.Eventf(ing, corev1.EventTypeWarning, "UpdateFailed", "Failed to update HTTPRoute: %v", err)
				return nil, fmt.Errorf("failed to update HTTPRoute: %w", err)
			}
			return updated, nil
		}
	}

	return httproute, err
}

func (c *Reconciler) reconcileTLS(
	ctx context.Context, ing *netv1alpha1.Ingress, tlsGw *gatewayv1alpha2.Gateway,
) error {
	recorder := controller.GetEventRecorder(ctx)

	gateway := metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Gateway",
			APIVersion: gatewayv1alpha2.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      tlsGw.Name,
			Namespace: tlsGw.Namespace,
		},
	}

	for _, tls := range ing.Spec.TLS {
		tls := tls

		secret := metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: corev1.SchemeGroupVersion.Version,
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      tls.SecretName,
				Namespace: tls.SecretNamespace,
			},
		}

		desired := resources.MakeReferenceGrant(ctx, ing, secret, gateway)

		rp, err := c.referencePolicyLister.ReferencePolicies(desired.Namespace).Get(desired.Name)

		if apierrs.IsNotFound(err) {
			rp, err = c.gwapiclient.GatewayV1alpha2().ReferencePolicies(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})

			if err != nil {
				recorder.Eventf(ing, corev1.EventTypeWarning, "CreationFailed", "Failed to create ReferencePolicy: %v", err)
				return fmt.Errorf("failed to create ReferencePolicy: %w", err)
			}
		} else if err != nil {
			return err
		}

		if !metav1.IsControlledBy(rp, ing) {
			recorder.Eventf(ing, corev1.EventTypeWarning, "NotOwned", "ReferencePolicy %s not owned by this object", desired.Name)
			return fmt.Errorf("ReferencePolicy %s not owned by %s", rp.Name, ing.Name)
		}

		if !equality.Semantic.DeepEqual(rp.Spec, desired.Spec) {
			update := rp.DeepCopy()
			update.Spec = desired.Spec

			_, err := c.gwapiclient.GatewayV1alpha2().ReferencePolicies(update.Namespace).Update(ctx, update, metav1.UpdateOptions{})
			if err != nil {
				recorder.Eventf(ing, corev1.EventTypeWarning, "UpdateFailed", "Failed to update ReferencePolicy: %v", err)
				return fmt.Errorf("failed to update ReferencePolicy: %w", err)
			}
		}
	}
	return nil
}

func (c *Reconciler) reconcileGateway(ctx context.Context, ing *netv1alpha1.Ingress) (*gatewayv1alpha2.Gateway, error) {
	recorder := controller.GetEventRecorder(ctx)
	logger := logging.FromContext(ctx)
	gatewayConfig := config.FromContext(ctx).Gateway
	// For now, we only support external gateways
	externalConfig := gatewayConfig.Gateways[netv1alpha1.IngressVisibilityExternalIP]

	httpGw, err := c.gatewayLister.Gateways(externalConfig.Gateway.Namespace).Get(externalConfig.Gateway.Name)
	if err != nil {
		return nil, fmt.Errorf("Unable to find HTTP Gateway %s: %w", externalConfig.Gateway, err)
	}

	desired := resources.MakeGateway(ctx, ing, httpGw.Status.Addresses)

	logger.Infof("Reconciling %s/%s", desired.Namespace, desired.Name)

	current, err := c.gatewayLister.Gateways(desired.Namespace).Get(desired.Name)
	if apierrs.IsNotFound(err) {
		logger.Infof("Creating gateway %+v", desired) // TODO: Remove
		return c.gwapiclient.GatewayV1alpha2().Gateways(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else if err != nil {
		recorder.Eventf(ing, corev1.EventTypeWarning, "CreateFailed", "Failed to create gateway %q: %s", desired.Name, err)
		return nil, err
	}

	if equality.Semantic.DeepDerivative(desired.Spec, current.Spec) {
		logger.Infof("No changes nedeed to %s/%s", desired.Namespace, desired.Name)
		return current, nil
	}

	update := current.DeepCopy()

	if !equality.Semantic.DeepEqual(desired.Spec.GatewayClassName, current.Spec.GatewayClassName) {
		update.Spec.GatewayClassName = desired.Spec.GatewayClassName
		logger.Infof("GatewayClassName mismatch in %s/%s", desired.Namespace, desired.Name)
	}

	if !equality.Semantic.DeepEqual(desired.Spec.Addresses, current.Spec.Addresses) {
		update.Spec.Addresses = desired.Spec.Addresses
		logger.Infof("Updating Addresses: %+v", update.Spec.Addresses)
	}

	if !equality.Semantic.DeepEqual(desired.Spec.Listeners, current.Spec.Listeners) {
		update.Spec.Listeners = desired.Spec.Listeners
		for i := range current.Spec.Listeners {
			if len(desired.Spec.Listeners) < i {
				continue
			}
			o := current.Spec.Listeners[i]
			n := current.Spec.Listeners[i]

			prefix := fmt.Sprintf("GW %s/%s, Listener %d", current.Namespace, current.Name, i)
			if equality.Semantic.DeepEqual(o, n) {
				logger.Infof("%s --- EQUAL", prefix)
				continue
			}
			if !equality.Semantic.DeepEqual(o.TLS, n.TLS) {
				logger.Infof("%s OLD: %+v   NEW: %+v", prefix, o.TLS, n.TLS)
			}
			if !equality.Semantic.DeepEqual(o.TLS.CertificateRefs[0], n.TLS.CertificateRefs[0]) {
				logger.Infof("%s OLD: %+v   NEW: %+v", prefix, o.TLS.CertificateRefs[0], n.TLS.CertificateRefs[0])
			}
			if !equality.Semantic.DeepEqual(o.AllowedRoutes, n.AllowedRoutes) {
				logger.Infof("%s OLD: %+v   NEW: %+v", prefix, o.TLS, n.TLS)
			}

		}
	}

	// TODO: annotations / labels
	logger.Infof("Old: %+v", current.Spec)
	logger.Infof("New: %+v", desired.Spec)

	recorder.Eventf(ing, corev1.EventTypeWarning, "UpdateGatewaySpec", "Updated Gateway")

	return c.gwapiclient.GatewayV1alpha2().Gateways(update.Namespace).Update(ctx, update, metav1.UpdateOptions{})
}
