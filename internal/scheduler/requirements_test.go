package scheduler

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDefaultEphemeralStorage(t *testing.T) {
	expected := resource.MustParse("40Gi")
	actual := resource.MustParse(DefaultEphemeralStorage)

	if !expected.Equal(actual) {
		t.Errorf("Expected DefaultEphemeralStorage to be %v, got %v", expected, actual)
	}
}
