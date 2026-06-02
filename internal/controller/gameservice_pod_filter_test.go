package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFilterServiceableConnectorPods(t *testing.T) {
	pods := []corev1.Pod{
		podWithPhase("running", corev1.PodRunning),
		podWithPhase("pending", corev1.PodPending),
		podWithPhase("failed", corev1.PodFailed),
		podWithPhase("succeeded", corev1.PodSucceeded),
		podWithPhase("unknown", corev1.PodUnknown),
	}

	filtered := filterServiceableConnectorPods(pods)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 serviceable pods, got %d", len(filtered))
	}
	if filtered[0].Name != "running" || filtered[1].Name != "pending" {
		t.Fatalf("expected running and pending pods, got %q and %q", filtered[0].Name, filtered[1].Name)
	}
}

func podWithPhase(name string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}
