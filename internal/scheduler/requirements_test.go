package scheduler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDefaultEphemeralStorage(t *testing.T) {
	expected := resource.MustParse("40Gi")
	actual := resource.MustParse(DefaultEphemeralStorage)

	if !expected.Equal(actual) {
		t.Errorf("Expected DefaultEphemeralStorage to be %v, got %v", expected, actual)
	}
}

func TestGetPodRequirements_Storage(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:              resource.MustParse("100m"),
							corev1.ResourceMemory:           resource.MustParse("128Mi"),
							corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
						},
					},
				},
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:              resource.MustParse("200m"),
							corev1.ResourceMemory:           resource.MustParse("256Mi"),
							corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
						},
					},
				},
			},
		},
	}

	reqs := GetPodRequirements(pod)

	expectedStorage := resource.MustParse("3Gi")
	if reqs.EphemeralStorage.Cmp(expectedStorage) != 0 {
		t.Errorf("Expected ephemeral storage %v, got %v", expectedStorage, reqs.EphemeralStorage)
	}
}
