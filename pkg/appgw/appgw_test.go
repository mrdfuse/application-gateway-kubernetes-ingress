package appgw

import (
	go_flag "flag"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2018-06-01/network"
	"github.com/Azure/go-autorest/autorest/to"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	testclient "k8s.io/client-go/kubernetes/fake"

	"github.com/Azure/application-gateway-kubernetes-ingress/pkg/k8scontext"
)

var _ = Describe("Tests `appgw.ConfigBuilder`", func() {
	var k8sClient kubernetes.Interface
	var ctxt *k8scontext.Context
	var configBuilder ConfigBuilder

	domainName := "hello.com"
	ingressNS := "test-ingress-controller"
	ingressName := "hello-world"
	serviceName := "hello-world"

	// Frontend and Backend port.
	servicePort := int32(80)
	backendPort := int32(1356)

	// Endpoints
	endpoint1 := "1.1.1.1"
	endpoint2 := "1.1.1.2"
	endpoint3 := "1.1.1.3"

	// Paths
	hiPath := "/hi"

	// Create the "test-ingress-controller" namespace.
	// We will create all our resources under this namespace.
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ingressNS,
		},
	}

	// Create the Ingress resource.
	ingress := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingressName,
			Namespace: ingressNS,
			Annotations: map[string]string{
				k8scontext.IngressClass: k8scontext.IngressControllerName,
			},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{
				{
					Host: domainName,
					IngressRuleValue: v1beta1.IngressRuleValue{
						HTTP: &v1beta1.HTTPIngressRuleValue{
							Paths: []v1beta1.HTTPIngressPath{
								{
									Path: hiPath,
									Backend: v1beta1.IngressBackend{
										ServiceName: serviceName,
										ServicePort: intstr.IntOrString{
											Type:   intstr.Int,
											IntVal: int32(servicePort),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: ingressNS,
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name: "frontendPort",
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: backendPort,
					},
					Protocol: v1.ProtocolTCP,
					Port:     servicePort,
				},
			},
			Selector: map[string]string{"app": "frontend"},
		},
	}

	// Ideally we should be creating the `pods` resource instead of the `endpoints` resource
	// and allowing the k8s API server to create the `endpoints` resource which we end up consuming.
	// However since we are using a fake k8s client the resources are dumb which forces us to create the final
	// expected resource manually.
	endpoints := &v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: ingressNS,
		},
		Subsets: []v1.EndpointSubset{
			{
				Addresses: []v1.EndpointAddress{
					{
						IP: endpoint1,
					},
					{
						IP: endpoint2,
					},
					{
						IP: endpoint3,
					},
				},
				Ports: []v1.EndpointPort{
					{
						Name:     "frontend",
						Port:     backendPort,
						Protocol: v1.ProtocolTCP,
					},
				},
			},
		},
	}

	go_flag.Lookup("logtostderr").Value.Set("true")
	go_flag.Set("v", "3")

	BeforeEach(func() {
		// Create the mock K8s client.
		k8sClient = testclient.NewSimpleClientset()

		_, err := k8sClient.CoreV1().Namespaces().Create(ns)
		Expect(err).Should(BeNil(), "Unable to create the namespace %s: %v", ingressNS, err)

		_, err = k8sClient.Extensions().Ingresses(ingressNS).Create(ingress)
		Expect(err).Should(BeNil(), "Unabled to create ingress resource due to: %v", err)

		// Create the service.
		_, err = k8sClient.CoreV1().Services(ingressNS).Create(service)
		Expect(err).Should(BeNil(), "Unabled to create service resource due to: %v", err)

		// Create the endpoints associated with this service.
		_, err = k8sClient.CoreV1().Endpoints(ingressNS).Create(endpoints)
		Expect(err).Should(BeNil(), "Unabled to create endpoints resource due to: %v", err)

		// Create a `k8scontext` to start listiening to ingress resources.
		ctxt = k8scontext.NewContext(k8sClient, ingressNS, 1000*time.Second)
		Expect(ctxt).ShouldNot(BeNil(), "Unable to create `k8scontext`")

		// Initialize the `ConfigBuilder`
		configBuilder = NewConfigBuilder(ctxt, &Identifier{}, &network.ApplicationGatewayPropertiesFormat{})

		builder, ok := configBuilder.(*appGwConfigBuilder)
		Expect(ok).Should(BeTrue(), "Unable to get the more specific configBuilder implementation")

		// Since this is a mock the `Application Gateway v2` does not have a public IP. During configuration process
		// the controller would expect the `Application Gateway v2` to have some public IP before it starts generating
		// configuration for the application gateway, hence creating this dummy configuration in the application gateway configuration.
		builder.appGwConfig.FrontendIPConfigurations = &[]network.ApplicationGatewayFrontendIPConfiguration{
			{
				Name: to.StringPtr("*"),
				Etag: to.StringPtr("*"),
				ID:   to.StringPtr("*"),
			},
		}
	})

	Context("Tests Application Gateway Configuration", func() {
		It("Should be able to create Application Gateway Configuration from Ingress", func() {
			// Start the informers. This will sync the cache with the latest ingress.
			ctxt.Run()

			// Get all the ingresses
			ingressList := ctxt.GetHTTPIngressList()
			// There should be only one ingress
			Expect(len(ingressList)).To(Equal(1), "Expected only one ingress resource but got: %d", len(ingressList))
			// Make sure it is the ingress we stored.
			Expect(ingressList[0]).To(Equal(ingress))

			// Add HTTP settings.
			configBuilder, err := configBuilder.BackendHTTPSettingsCollection(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the HTTP Settings: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW := configBuilder.Build()
			// We will have a default HTTP setting that gets added, and an HTTP setting corresponding to port `backendPort`
			Expect(len(*appGW.BackendHTTPSettingsCollection)).To(Equal(2), "Expected two HTTP setting, but got: %d", len(*appGW.BackendHTTPSettingsCollection))

			expectedBackend := &ingress.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend
			httpSettingsName := generateHTTPSettingsName(generateBackendID(ingress, expectedBackend).serviceFullName(), fmt.Sprintf("%d", servicePort), backendPort)
			httpSettings := &network.ApplicationGatewayBackendHTTPSettings{
				Etag: to.StringPtr("*"),
				Name: &httpSettingsName,
				ApplicationGatewayBackendHTTPSettingsPropertiesFormat: &network.ApplicationGatewayBackendHTTPSettingsPropertiesFormat{
					Protocol: network.HTTP,
					Port:     &backendPort,
				},
			}

			// Test the default backend HTTP settings.
			Expect((*appGW.BackendHTTPSettingsCollection)[0]).To(Equal(defaultBackendHTTPSettings()))
			// Test the ingress backend HTTP setting that we installed.
			Expect((*appGW.BackendHTTPSettingsCollection)[1]).To(Equal(*httpSettings))

			// Add backend address pools. We need the HTTP settings before we can add the backend address pools.
			configBuilder, err = configBuilder.BackendAddressPools(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the backend address pools: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW = configBuilder.Build()
			// We will have a default backend address pool that gets added, and a backend pool corresponding to our service.
			Expect(len(*appGW.BackendAddressPools)).To(Equal(2), "Expected two backend address pools, but got: %d", len(*appGW.BackendAddressPools))

			addressPoolName := generateAddressPoolName(generateBackendID(ingress, expectedBackend).serviceFullName(), fmt.Sprintf("%d", servicePort), backendPort)
			addressPoolAddresses := [](network.ApplicationGatewayBackendAddress){{IPAddress: &endpoint1}, {IPAddress: &endpoint2}, {IPAddress: &endpoint3}}

			addressPool := &network.ApplicationGatewayBackendAddressPool{
				Etag: to.StringPtr("*"),
				Name: &addressPoolName,
				ApplicationGatewayBackendAddressPoolPropertiesFormat: &network.ApplicationGatewayBackendAddressPoolPropertiesFormat{
					BackendAddresses: &addressPoolAddresses,
				},
			}

			// Test the default backend address pool.
			Expect((*appGW.BackendAddressPools)[0]).To(Equal(defaultBackendAddressPool()))
			// Test the ingress backend address pool that we installed.
			Expect((*appGW.BackendAddressPools)[1]).To(Equal(*addressPool))

			// Add the listeners. We need the backend address pools before we can add HTTP listeners.
			configBuilder, err = configBuilder.HTTPListeners(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the HTTP listeners: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW = configBuilder.Build()
			// Ingress allows listeners on port 80 or port 443. Therefore in this particular case we would have only a single listener
			Expect(len(*appGW.HTTPListeners)).To(Equal(1), "Expected a single HTTP listener, but got: %d", len(*appGW.HTTPListeners))

			// Test the listener.
			appGwIdentifier := Identifier{}
			frontendPortID := appGwIdentifier.frontendPortID(generateFrontendPortName(80))
			httpListenerName := generateHTTPListenerName(frontendListenerIdentifier{80, domainName})
			httpListener := &network.ApplicationGatewayHTTPListener{
				Etag: to.StringPtr("*"),
				Name: &httpListenerName,
				ApplicationGatewayHTTPListenerPropertiesFormat: &network.ApplicationGatewayHTTPListenerPropertiesFormat{
					FrontendIPConfiguration: resourceRef("*"),
					FrontendPort:            resourceRef(frontendPortID),
					Protocol:                network.HTTP,
					HostName:                &domainName,
				},
			}

			Expect((*appGW.HTTPListeners)[0]).To(Equal(*httpListener))

			// RequestRoutingRules depends on the previous operations
			configBuilder, err = configBuilder.RequestRoutingRules(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the routing rules: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW = configBuilder.Build()
			Expect(len(*appGW.RequestRoutingRules)).To(Equal(1), "Expected one routing rule, but got: %d", len(*appGW.RequestRoutingRules))
			Expect(*((*appGW.RequestRoutingRules)[0].Name)).To(Equal(generateRequestRoutingRuleName(frontendListenerIdentifier{80, domainName})))
			Expect((*appGW.RequestRoutingRules)[0].RuleType).To(Equal(network.PathBasedRouting))

			// Check the `urlPathMaps`
			Expect(len(*appGW.URLPathMaps)).To(Equal(1), "Expected one URL path map routing, but got: %d", len(*appGW.URLPathMaps))
			Expect(*((*appGW.URLPathMaps)[0].Name)).To(Equal(generateURLPathMapName(frontendListenerIdentifier{80, domainName})))
			// Check the `pathRule` stored within the `urlPathMap`.
			Expect(len(*((*appGW.URLPathMaps)[0].PathRules))).To(Equal(1), "Expected one path based rule, but got: %d", len(*((*appGW.URLPathMaps)[0].PathRules)))

			pathRule := (*((*appGW.URLPathMaps)[0].PathRules))[0]
			Expect(len(*(pathRule.Paths))).To(Equal(1), "Expected a single path in path-based rules, but got: %d", len(*(pathRule.Paths)))
			// Check the exact path that was set.
			Expect((*pathRule.Paths)[0]).To(Equal("/hi"))
		})
	})

	Context("Tests Ingress Controller when Service doesn't exists", func() {
		It("Should be able to create Application Gateway Configuration from Ingress with empty backend pool.", func() {
			// Delete the service
			options := &metav1.DeleteOptions{}
			err := k8sClient.CoreV1().Services(ingressNS).Delete(serviceName, options)
			Expect(err).Should(BeNil(), "Unable to delete service resource due to: %v", err)

			// Delete the Endpoint
			err = k8sClient.CoreV1().Endpoints(ingressNS).Delete(serviceName, options)
			Expect(err).Should(BeNil(), "Unable to delete endpoint resource due to: %v", err)

			// Start the informers. This will sync the cache with the latest ingress.
			ctxt.Run()

			// Get all the ingresses
			ingressList := ctxt.GetHTTPIngressList()
			// There should be only one ingress
			Expect(len(ingressList)).To(Equal(1), "Expected only one ingress resource but got: %d", len(ingressList))
			// Make sure it is the ingress we stored.
			Expect(ingressList[0]).To(Equal(ingress))

			// Add HTTP settings.
			configBuilder, err := configBuilder.BackendHTTPSettingsCollection(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the HTTP Settings: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW := configBuilder.Build()
			// We will have a default HTTP setting that gets added, and an HTTP setting corresponding to port `backendPort`
			Expect(len(*appGW.BackendHTTPSettingsCollection)).To(Equal(2), "Expected two HTTP setting, but got: %d", len(*appGW.BackendHTTPSettingsCollection))

			expectedBackend := &ingress.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend
			httpSettingsName := generateHTTPSettingsName(generateBackendID(ingress, expectedBackend).serviceFullName(), fmt.Sprintf("%d", servicePort), servicePort)
			httpSettings := &network.ApplicationGatewayBackendHTTPSettings{
				Etag: to.StringPtr("*"),
				Name: &httpSettingsName,
				ApplicationGatewayBackendHTTPSettingsPropertiesFormat: &network.ApplicationGatewayBackendHTTPSettingsPropertiesFormat{
					Protocol: network.HTTP,
					Port:     &servicePort,
				},
			}

			// Test the default backend HTTP settings.
			Expect((*appGW.BackendHTTPSettingsCollection)[0]).To(Equal(defaultBackendHTTPSettings()))
			// Test the ingress backend HTTP setting that we installed.
			Expect((*appGW.BackendHTTPSettingsCollection)[1]).To(Equal(*httpSettings))

			// Add backend address pools. We need the HTTP settings before we can add the backend address pools.
			configBuilder, err = configBuilder.BackendAddressPools(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the backend address pools: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW = configBuilder.Build()
			// We will have a default backend address pool that gets added, and a backend pool corresponding to our service.
			Expect(len(*appGW.BackendAddressPools)).To(Equal(1), "Expected two backend address pools, but got: %d", len(*appGW.BackendAddressPools))

			// Test the default backend address pool.
			Expect((*appGW.BackendAddressPools)[0]).To(Equal(defaultBackendAddressPool()))

			// Add the listeners. We need the backend address pools before we can add HTTP listeners.
			configBuilder, err = configBuilder.HTTPListeners(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the HTTP listeners: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW = configBuilder.Build()
			// Ingress allows listners on port 80 or port 443. Therefore in this particular case we would have only a single listener
			Expect(len(*appGW.HTTPListeners)).To(Equal(1), "Expected a single HTTP listener, but got: %d", len(*appGW.HTTPListeners))

			// Test the listener.
			appGwIdentifier := Identifier{}
			frontendPortID := appGwIdentifier.frontendPortID(generateFrontendPortName(80))
			httpListenerName := generateHTTPListenerName(frontendListenerIdentifier{80, domainName})
			httpListener := &network.ApplicationGatewayHTTPListener{
				Etag: to.StringPtr("*"),
				Name: &httpListenerName,
				ApplicationGatewayHTTPListenerPropertiesFormat: &network.ApplicationGatewayHTTPListenerPropertiesFormat{
					FrontendIPConfiguration: resourceRef("*"),
					FrontendPort:            resourceRef(frontendPortID),
					Protocol:                network.HTTP,
					HostName:                &domainName,
				},
			}

			Expect((*appGW.HTTPListeners)[0]).To(Equal(*httpListener))

			// RequestRoutingRules depends on the previous operations
			configBuilder, err = configBuilder.RequestRoutingRules(ingressList)
			Expect(err).Should(BeNil(), "Error in generating the routing rules: %v", err)

			// Retrieve the implementation of the `ConfigBuilder` interface.
			appGW = configBuilder.Build()
			Expect(len(*appGW.RequestRoutingRules)).To(Equal(1), "Expected one routing rule, but got: %d", len(*appGW.RequestRoutingRules))
			Expect(*((*appGW.RequestRoutingRules)[0].Name)).To(Equal(generateRequestRoutingRuleName(frontendListenerIdentifier{80, domainName})))
			Expect((*appGW.RequestRoutingRules)[0].RuleType).To(Equal(network.PathBasedRouting))

			// Check the `urlPathMaps`
			Expect(len(*appGW.URLPathMaps)).To(Equal(1), "Expected one URL path map routing, but got: %d", len(*appGW.URLPathMaps))
			Expect(*((*appGW.URLPathMaps)[0].Name)).To(Equal(generateURLPathMapName(frontendListenerIdentifier{80, domainName})))
			// Check the `pathRule` stored within the `urlPathMap`.
			Expect(len(*((*appGW.URLPathMaps)[0].PathRules))).To(Equal(1), "Expected one path based rule, but got: %d", len(*((*appGW.URLPathMaps)[0].PathRules)))

			pathRule := (*((*appGW.URLPathMaps)[0].PathRules))[0]
			Expect(len(*(pathRule.Paths))).To(Equal(1), "Expected a single path in path-based rules, but got: %d", len(*(pathRule.Paths)))
			// Check the exact path that was set.
			Expect((*pathRule.Paths)[0]).To(Equal("/hi"))
		})
	})
})
