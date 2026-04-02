package discovery

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseProviderID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		providerID   string
		wantProvider string
		wantRegion   string
		wantInstance string
	}{
		{name: "aliyun", providerID: "aliyun:///cn-hangzhou/i-bp123456", wantProvider: "aliyun", wantRegion: "cn-hangzhou", wantInstance: "i-bp123456"},
		{name: "aliyun ack format", providerID: "ap-southeast-1.i-t4n8li1ek5abarylythw", wantProvider: "aliyun", wantRegion: "ap-southeast-1", wantInstance: "i-t4n8li1ek5abarylythw"},
		{name: "aws", providerID: "aws:///us-west-2/i-0abc123", wantProvider: "aws", wantRegion: "us-west-2", wantInstance: "i-0abc123"},
		{name: "gcp", providerID: "gce:///my-project/us-central1-a/gke-node", wantProvider: "gcp", wantRegion: "us-central1-a", wantInstance: "gke-node"},
		{name: "invalid", providerID: "bad-value"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider, region, instanceID := ParseProviderID(tc.providerID)
			if provider != tc.wantProvider || region != tc.wantRegion || instanceID != tc.wantInstance {
				t.Fatalf("ParseProviderID() = (%q, %q, %q), want (%q, %q, %q)", provider, region, instanceID, tc.wantProvider, tc.wantRegion, tc.wantInstance)
			}
		})
	}
}

func TestNodeDiscoveryFiltersInvalidTargets(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset(
		newNode("worker-1", "aliyun:///cn-hangzhou/i-bp123456", "10.0.0.10", "39.0.0.10"),
		newNode("worker-2", "aws:///us-west-2/i-abc", "10.0.0.11", ""),
		newNode("worker-3", "", "10.0.0.12", ""),
	)

	discovery := NewNodeDiscovery(client, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := discovery.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	targets := discovery.List()
	if len(targets) != 1 {
		t.Fatalf("List() len = %d, want 1", len(targets))
	}

	target := targets[0]
	if target.NodeName != "worker-1" || target.InstanceID != "i-bp123456" || target.Region != "cn-hangzhou" {
		t.Fatalf("unexpected target = %+v", target)
	}
}

func TestNodeToTargetFallsBackToBetaZoneLabel(t *testing.T) {
	t.Parallel()

	node := newNode("worker-beta", "aliyun:///cn-hangzhou/i-bp654321", "10.0.0.20", "", map[string]string{
		v1.LabelFailureDomainBetaZone: "cn-hangzhou-i",
	})

	target, ok := nodeToTarget(node)
	if !ok {
		t.Fatal("nodeToTarget() ok = false, want true")
	}
	if target.Zone != "cn-hangzhou-i" {
		t.Fatalf("target.Zone = %q, want %q", target.Zone, "cn-hangzhou-i")
	}
}

func newNode(name, providerID, internalIP, externalIP string, labels ...map[string]string) *v1.Node {
	addresses := []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: internalIP}}
	if externalIP != "" {
		addresses = append(addresses, v1.NodeAddress{Type: v1.NodeExternalIP, Address: externalIP})
	}

	nodeLabels := map[string]string{
		v1.LabelTopologyZone: "cn-hangzhou-h",
	}
	if len(labels) > 0 {
		nodeLabels = labels[0]
	}

	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: nodeLabels,
		},
		Spec:   v1.NodeSpec{ProviderID: providerID},
		Status: v1.NodeStatus{Addresses: addresses},
	}
}
