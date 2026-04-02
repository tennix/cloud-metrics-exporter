package discovery

import (
	"context"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type DiskEnrichment struct {
	Matched      bool
	PV           string
	PVC          string
	Namespace    string
	Pod          string
	Workload     string
	WorkloadKind string
}

type VolumeEnricher struct {
	factory informers.SharedInformerFactory

	pvInformer  cache.SharedIndexInformer
	pvcInformer cache.SharedIndexInformer
	podInformer cache.SharedIndexInformer
	rsInformer  cache.SharedIndexInformer
}

func NewVolumeEnricher(client kubernetes.Interface, interval time.Duration) *VolumeEnricher {
	factory := informers.NewSharedInformerFactory(client, interval)

	return &VolumeEnricher{
		factory:     factory,
		pvInformer:  factory.Core().V1().PersistentVolumes().Informer(),
		pvcInformer: factory.Core().V1().PersistentVolumeClaims().Informer(),
		podInformer: factory.Core().V1().Pods().Informer(),
		rsInformer:  factory.Apps().V1().ReplicaSets().Informer(),
	}
}

func (e *VolumeEnricher) Start(ctx context.Context) error {
	e.factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), e.pvInformer.HasSynced, e.pvcInformer.HasSynced, e.podInformer.HasSynced, e.rsInformer.HasSynced) {
		return ctx.Err()
	}
	return nil
}

func (e *VolumeEnricher) LookupByDiskID(diskID string) DiskEnrichment {
	if diskID == "" {
		return DiskEnrichment{}
	}

	pv := e.findPVByDiskID(diskID)
	if pv == nil || pv.Spec.ClaimRef == nil {
		return DiskEnrichment{}
	}

	namespace := pv.Spec.ClaimRef.Namespace
	pvcName := pv.Spec.ClaimRef.Name
	pvcKey := namespace + "/" + pvcName
	if _, exists, _ := e.pvcInformer.GetStore().GetByKey(pvcKey); !exists {
		return DiskEnrichment{}
	}

	enrichment := DiskEnrichment{
		Matched:   true,
		PV:        pv.Name,
		PVC:       pvcName,
		Namespace: namespace,
	}

	pod := e.findPodByPVC(namespace, pvcName)
	if pod == nil {
		return enrichment
	}
	enrichment.Pod = pod.Name
	enrichment.Workload, enrichment.WorkloadKind = e.resolveWorkload(pod)

	return enrichment
}

func (e *VolumeEnricher) findPVByDiskID(diskID string) *v1.PersistentVolume {
	items := e.pvInformer.GetStore().List()
	for _, item := range items {
		pv, ok := item.(*v1.PersistentVolume)
		if !ok {
			continue
		}
		if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == diskID {
			return pv
		}
	}
	return nil
}

func (e *VolumeEnricher) findPodByPVC(namespace, pvcName string) *v1.Pod {
	items := e.podInformer.GetStore().List()
	matched := make([]*v1.Pod, 0)
	for _, item := range items {
		pod, ok := item.(*v1.Pod)
		if !ok || pod.Namespace != namespace {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == pvcName {
				matched = append(matched, pod)
				break
			}
		}
	}

	if len(matched) == 0 {
		return nil
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Name < matched[j].Name
	})
	return matched[0]
}

func (e *VolumeEnricher) resolveWorkload(pod *v1.Pod) (string, string) {
	owner := controllerOwnerRef(pod.OwnerReferences)
	if owner == nil {
		return "", ""
	}

	if owner.Kind != "ReplicaSet" {
		return owner.Name, owner.Kind
	}

	rsObj, exists, _ := e.rsInformer.GetStore().GetByKey(pod.Namespace + "/" + owner.Name)
	if !exists {
		return owner.Name, owner.Kind
	}

	rs, ok := rsObj.(*appsv1.ReplicaSet)
	if !ok {
		return owner.Name, owner.Kind
	}

	rsOwner := controllerOwnerRef(rs.OwnerReferences)
	if rsOwner == nil {
		return owner.Name, owner.Kind
	}

	if rsOwner.Kind == "Deployment" {
		return rsOwner.Name, rsOwner.Kind
	}

	return owner.Name, owner.Kind
}

func controllerOwnerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}
