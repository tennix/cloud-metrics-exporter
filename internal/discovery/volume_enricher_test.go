package discovery

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVolumeEnricherLookupByDiskID(t *testing.T) {
	t.Parallel()

	controller := true
	client := fake.NewSimpleClientset(
		&v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-disk-a"},
			Spec: v1.PersistentVolumeSpec{
				ClaimRef: &v1.ObjectReference{Namespace: "app", Name: "data"},
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{VolumeHandle: "d-123"},
				},
			},
		},
		&v1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "app"}},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-rs",
				Namespace: "app",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "Deployment",
					Name:       "web",
					Controller: &controller,
				}},
			},
		},
		&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-pod",
				Namespace: "app",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "ReplicaSet",
					Name:       "web-rs",
					Controller: &controller,
				}},
			},
			Spec: v1.PodSpec{Volumes: []v1.Volume{{
				Name: "data",
				VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: "data",
				}},
			}}},
		},
	)

	enricher := NewVolumeEnricher(client, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := enricher.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	enrichment := enricher.LookupByDiskID("d-123")
	if !enrichment.Matched {
		t.Fatal("LookupByDiskID() Matched = false, want true")
	}
	if enrichment.PV != "pv-disk-a" || enrichment.PVC != "data" || enrichment.Namespace != "app" {
		t.Fatalf("unexpected PV/PVC enrichment = %+v", enrichment)
	}
	if enrichment.Pod != "web-pod" {
		t.Fatalf("enrichment.Pod = %q, want web-pod", enrichment.Pod)
	}
	if enrichment.Workload != "web" || enrichment.WorkloadKind != "Deployment" {
		t.Fatalf("unexpected workload enrichment = %+v", enrichment)
	}
}

func TestVolumeEnricherLookupByDiskIDUnmatched(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	enricher := NewVolumeEnricher(client, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := enricher.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	enrichment := enricher.LookupByDiskID("d-missing")
	if enrichment.Matched {
		t.Fatalf("LookupByDiskID() Matched = true, want false: %+v", enrichment)
	}
}
