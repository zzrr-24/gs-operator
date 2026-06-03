package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type IngressConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9\-\.]*[a-zA-Z0-9])?$`
	Host string `json:"host"`

	// +kubebuilder:validation:MinLength=1
	IngressClassName string `json:"ingressClassName,omitempty"`

	// +kubebuilder:validation:Enum=Prefix;Exact;ImplementationSpecific
	PathType string `json:"pathType"`

	// +kubebuilder:validation:MinLength=1
	PathPrefix string `json:"pathPrefix"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	TLS         *TLSConfig        `json:"tls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// +kubebuilder:validation:Enum=Ingress;Gateway
type TrafficMode string

const (
	TrafficModeIngress TrafficMode = "Ingress"
	TrafficModeGateway TrafficMode = "Gateway"
)

type GatewayConfig struct {
	// +kubebuilder:validation:Required
	ParentRef GatewayParentRef `json:"parentRef"`
}

type GatewayParentRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace,omitempty"`

	// +kubebuilder:validation:MinLength=1
	SectionName string `json:"sectionName,omitempty"`
}

type TLSConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
}

type DeployGroupConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=blue;green
	Role string `json:"role"`

	Active bool `json:"active"`
}

type RetentionConfig struct {
	Enabled bool `json:"enabled"`

	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*h$`
	DefaultDuration string `json:"defaultDuration"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.trafficMode) || self.trafficMode != 'Ingress' || has(self.ingress.ingressClassName)",message="ingress.ingressClassName is required when trafficMode is Ingress"
// +kubebuilder:validation:XValidation:rule="!has(self.trafficMode) || self.trafficMode != 'Gateway' || has(self.gateway)",message="gateway is required when trafficMode is Gateway"
type GameServiceSpec struct {
	// +kubebuilder:default=Ingress
	TrafficMode TrafficMode `json:"trafficMode,omitempty"`

	// +kubebuilder:validation:Required
	Ingress IngressConfig `json:"ingress"`

	Gateway *GatewayConfig `json:"gateway,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PodLabelKey string `json:"podLabelKey"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PodLabelValue string `json:"podLabelValue"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ConnectorNamespace string `json:"connectorNamespace"`

	// +kubebuilder:validation:Required
	DeployGroup DeployGroupConfig `json:"deployGroup"`

	Retention *RetentionConfig `json:"retention,omitempty"`
}

type GameServiceStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ConnectorCount     int32              `json:"connectorCount,omitempty"`
	ConnectorImage     string             `json:"connectorImage,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.deployGroup.role"
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=".spec.deployGroup.active"
// +kubebuilder:printcolumn:name="Pods",type=integer,JSONPath=".status.connectorCount"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".status.connectorImage"
type GameService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GameServiceSpec   `json:"spec"`
	Status            GameServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type GameServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GameService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GameService{}, &GameServiceList{})
}
