package discovery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type NodeTarget struct {
	NodeName   string
	InternalIP string
	ExternalIP string
	InstanceID string
	Region     string
	Zone       string
	Provider   string
}

type NodeDiscovery struct {
	client   kubernetes.Interface
	interval time.Duration

	mu      sync.RWMutex
	targets map[string]NodeTarget
}

func NewNodeDiscovery(client kubernetes.Interface, interval time.Duration) *NodeDiscovery {
	return &NodeDiscovery{
		client:   client,
		interval: interval,
		targets:  make(map[string]NodeTarget),
	}
}

func (d *NodeDiscovery) Start(ctx context.Context) error {
	factory := informers.NewSharedInformerFactory(d.client, d.interval)
	informer := factory.Core().V1().Nodes().Informer()

	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			d.upsertNode(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			d.upsertNode(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			d.deleteNode(obj)
		},
	})
	if err != nil {
		return fmt.Errorf("add node event handler: %w", err)
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("wait for node informer sync: %w", ctx.Err())
	}

	return nil
}

func (d *NodeDiscovery) List() []NodeTarget {
	d.mu.RLock()
	defer d.mu.RUnlock()

	targets := make([]NodeTarget, 0, len(d.targets))
	for _, target := range d.targets {
		targets = append(targets, target)
	}

	return targets
}

func (d *NodeDiscovery) upsertNode(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		return
	}

	target, ok := nodeToTarget(node)
	d.mu.Lock()
	defer d.mu.Unlock()
	if !ok {
		delete(d.targets, node.Name)
		return
	}

	d.targets[node.Name] = target
}

func (d *NodeDiscovery) deleteNode(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		node, ok = tombstone.Obj.(*v1.Node)
		if !ok {
			return
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.targets, node.Name)
}

func nodeToTarget(node *v1.Node) (NodeTarget, bool) {
	provider, region, instanceID := ParseProviderID(node.Spec.ProviderID)
	if provider != "aliyun" || instanceID == "" || region == "" {
		return NodeTarget{}, false
	}

	return NodeTarget{
		NodeName:   node.Name,
		InternalIP: addressForType(node.Status.Addresses, v1.NodeInternalIP),
		ExternalIP: addressForType(node.Status.Addresses, v1.NodeExternalIP),
		InstanceID: instanceID,
		Region:     region,
		Zone:       zoneLabel(node.Labels),
		Provider:   provider,
	}, true
}

func zoneLabel(labels map[string]string) string {
	if zone := labels[v1.LabelTopologyZone]; zone != "" {
		return zone
	}
	return labels[v1.LabelFailureDomainBetaZone]
}

func addressForType(addresses []v1.NodeAddress, addressType v1.NodeAddressType) string {
	for _, addr := range addresses {
		if addr.Type == addressType {
			return addr.Address
		}
	}
	return ""
}

func ParseProviderID(providerID string) (provider, region, instanceID string) {
	if providerID == "" {
		return "", "", ""
	}

	if !strings.Contains(providerID, "://") {
		parts := strings.SplitN(providerID, ".", 2)
		if len(parts) == 2 && strings.HasPrefix(parts[1], "i-") {
			return "aliyun", parts[0], parts[1]
		}
		return "", "", ""
	}

	parts := strings.SplitN(providerID, "://", 2)
	if len(parts) != 2 {
		return "", "", ""
	}

	provider = parts[0]
	path := strings.TrimPrefix(parts[1], "/")
	segments := strings.Split(path, "/")

	switch provider {
	case "aws", "aliyun":
		if len(segments) >= 2 {
			return provider, segments[0], segments[1]
		}
	case "azure":
		return provider, "", providerID
	case "gce":
		if len(segments) >= 3 {
			return "gcp", segments[1], segments[2]
		}
	}

	return "", "", ""
}
