/*
Copyright 2022 The Knative Authors

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

package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"knative.dev/net-gateway-api/pkg/reconciler/ingress/config"
	netv1alpha1 "knative.dev/networking/pkg/apis/networking/v1alpha1"
	"knative.dev/pkg/kmeta"
)

func MakeGateway(ctx context.Context, ing *netv1alpha1.Ingress, addrs []gatewayv1alpha2.GatewayAddress) *gatewayv1alpha2.Gateway {
	gatewayConfig := config.FromContext(ctx).Gateway
	// For now, we only support external gateways
	externalConfig := gatewayConfig.Gateways[netv1alpha1.IngressVisibilityExternalIP]

	mode := gatewayv1alpha2.TLSModeTerminate
	selector := gatewayv1alpha2.NamespacesFromSelector

	name := "kn-" + ing.Namespace + "-" + ing.Name
	if len(name) > 63 {
		name = name[:63]
	}

	gw := gatewayv1alpha2.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: externalConfig.Gateway.Namespace,
			Labels:    ing.Labels,
			Annotations: kmeta.FilterMap(ing.GetAnnotations(), func(key string) bool {
				return key == corev1.LastAppliedConfigAnnotation
			}),
			// OwnerReferences: []metav1.OwnerReference{*kmeta.NewControllerRef(ing)},
		},
		Spec: gatewayv1alpha2.GatewaySpec{
			GatewayClassName: gatewayv1alpha2.ObjectName(externalConfig.GatewayClass),
			Listeners:        make([]gatewayv1alpha2.Listener, 0, len(ing.Spec.TLS)),
			Addresses:        addrs,
		},
	}

	for _, tls := range ing.Spec.TLS {
		for _, host := range tls.Hosts {
			host := host // Avoid loop aliasing
			l := gatewayv1alpha2.Listener{
				Name:     gatewayv1alpha2.SectionName(host),
				Hostname: (*gatewayv1alpha2.Hostname)(&host),
				Port:     443,
				Protocol: gatewayv1alpha2.HTTPSProtocolType,
				TLS: &gatewayv1alpha2.GatewayTLSConfig{
					Mode: &mode,
					CertificateRefs: []*gatewayv1alpha2.SecretObjectReference{{
						Group:     (*gatewayv1alpha2.Group)(pointer.String("")),
						Kind:      (*gatewayv1alpha2.Kind)(pointer.String("Secret")),
						Name:      gatewayv1alpha2.ObjectName(tls.SecretName),
						Namespace: (*gatewayv1alpha2.Namespace)(&tls.SecretNamespace),
					}},
				},
				AllowedRoutes: &gatewayv1alpha2.AllowedRoutes{
					Namespaces: &gatewayv1alpha2.RouteNamespaces{
						From: &selector,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								corev1.LabelMetadataName: ing.Namespace,
							},
						},
					},
					Kinds: []gatewayv1alpha2.RouteGroupKind{},
				},
			}
			gw.Spec.Listeners = append(gw.Spec.Listeners, l)
		}
	}
	return &gw
}
