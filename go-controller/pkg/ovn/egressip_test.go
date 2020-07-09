package ovn

import (
	"context"
	"net"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	egressipv1 "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressip/v1"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilnet "k8s.io/utils/net"
)

func newEgressIPMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		UID:  types.UID(name),
		Name: name,
		Labels: map[string]string{
			"name": name,
		},
	}
}

var node1Name = "node1"
var node2Name = "node2"

func setupNode(nodeName string, ipNets []string, mockAllocationIPs []string) eNode {
	var v4Subnet, v6Subnet *net.IPNet
	for _, ipNet := range ipNets {
		_, net, _ := net.ParseCIDR(ipNet)
		if utilnet.IsIPv6CIDR(net) {
			v6Subnet = net
		} else {
			v4Subnet = net
		}
	}

	mockAllcations := map[string]bool{}
	for _, mockAllocationIP := range mockAllocationIPs {
		mockAllcations[net.ParseIP(mockAllocationIP).String()] = true
	}

	node := eNode{
		v4Subnet:    v4Subnet,
		v6Subnet:    v6Subnet,
		allocations: mockAllcations,
		name:        nodeName,
	}
	return node
}

var _ = Describe("OVN master EgressIP Operations", func() {
	var (
		app     *cli.App
		fakeOvn *FakeOVN
		tExec   *ovntest.FakeExec
	)

	BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags

		tExec = ovntest.NewFakeExec()
		fakeOvn = NewFakeOVN(tExec)
	})

	AfterEach(func() {
		fakeOvn.shutdown()
	})

	Context("WatchEgressNodes", func() {

		It("should populated egress node data as they are tagged `egress assignable` with variants of IPv4/IPv6", func() {
			app.Action = func(ctx *cli.Context) error {

				node1IPv4 := "192.168.128.202/24"
				node1IPv6 := "0:0:0:0:0:feff:c0a8:8e0c/64"
				node2IPv4 := "192.168.126.51/24"

				node1 := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": node1IPv4,
						"k8s.ovn.org/node-primary-cidr-ipv6": node1IPv6,
					},
				}}
				node2 := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: "node2",
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": node2IPv4,
					},
				}}
				fakeOvn.start(ctx, nil, &v1.NodeList{
					Items: []v1.Node{},
				})

				err := fakeOvn.controller.WatchEgressNodes()
				Expect(err).NotTo(HaveOccurred())
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(0))

				node1.Labels = map[string]string{
					"k8s.ovn.org/egress-assignable": "",
				}

				_, ip1V4Sub, err := net.ParseCIDR(node1IPv4)
				_, ip1V6Sub, err := net.ParseCIDR(node1IPv6)
				_, ip2V4Sub, err := net.ParseCIDR(node2IPv4)

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Create(context.TODO(), &node1, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(1))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node1.Name))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v4Subnet).To(Equal(ip1V4Sub))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v6Subnet).To(Equal(ip1V6Sub))

				node2.Labels = map[string]string{
					"k8s.ovn.org/egress-assignable": "",
				}

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Create(context.TODO(), &node2, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(2))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node1.Name))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node2.Name))
				Expect(fakeOvn.controller.eIPAllocator[node2.Name].v4Subnet).To(Equal(ip2V4Sub))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v4Subnet).To(Equal(ip1V4Sub))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v6Subnet).To(Equal(ip1V6Sub))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should skip populating egress node data for nodes that have incorrect IP address", func() {
			app.Action = func(ctx *cli.Context) error {

				nodeIPv4 := "192.168.126.510/24"
				nodeIPv6 := "0:0:0:0:0:feff:c0a8:8e0c/64"
				node := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: "myNode",
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": nodeIPv4,
						"k8s.ovn.org/node-primary-cidr-ipv6": nodeIPv6,
					},
				}}
				fakeOvn.start(ctx, nil, &v1.NodeList{
					Items: []v1.Node{node},
				})

				err := fakeOvn.controller.WatchEgressNodes()
				Expect(err).NotTo(HaveOccurred())
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(0))

				node.Labels = map[string]string{
					"k8s.ovn.org/egress-assignable": "",
				}

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Update(context.TODO(), &node, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred())
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

	})

	Context("WatchEgressNodes running with WatchEgressIP", func() {

		It("should treat un-assigned EgressIPs when it is tagged", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP := "192.168.126.101"
				nodeIPv4 := "192.168.126.51/24"
				nodeIPv6 := "0:0:0:0:0:feff:c0a8:8e0c/64"

				node := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: node1Name,
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": nodeIPv4,
						"k8s.ovn.org/node-primary-cidr-ipv6": nodeIPv6,
					},
				}}

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
					Status: []egressipv1.EgressIPStatus{},
				}

				fakeOvn.start(ctx,
					[]runtime.Object{
						&eIP,
					}, &v1.NodeList{
						Items: []v1.Node{node},
					})

				err := fakeOvn.controller.WatchEgressNodes()
				Expect(err).NotTo(HaveOccurred())
				err = fakeOvn.controller.WatchEgressIP()
				Expect(err).NotTo(HaveOccurred())

				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(0))
				Eventually(eIP.Status).Should(HaveLen(0))

				node.Labels = map[string]string{
					"k8s.ovn.org/egress-assignable": "",
				}

				_, ipV4Sub, err := net.ParseCIDR(nodeIPv4)
				_, ipV6Sub, err := net.ParseCIDR(nodeIPv6)

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Update(context.TODO(), &node, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node.Name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))

				Expect(fakeOvn.controller.eIPAllocator).To(HaveLen(1))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node.Name))
				Expect(fakeOvn.controller.eIPAllocator[node.Name].v4Subnet).To(Equal(ipV4Sub))
				Expect(fakeOvn.controller.eIPAllocator[node.Name].v6Subnet).To(Equal(ipV6Sub))

				cacheCount := 0
				fakeOvn.controller.egressAssignmentRetry.Range(func(key, value interface{}) bool {
					cacheCount++
					return true
				})

				Expect(cacheCount).To(Equal(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should only get assigned EgressIPs which matches their subnet when the node is tagged", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP := "192.168.126.101"
				node1IPv4 := "192.168.128.202/24"
				node1IPv6 := "0:0:0:0:0:feff:c0a8:8e0c/64"
				node2IPv4 := "192.168.126.51/24"

				node1 := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: node1Name,
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": node1IPv4,
						"k8s.ovn.org/node-primary-cidr-ipv6": node1IPv6,
					},
				}}
				node2 := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: node2Name,
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": node2IPv4,
					},
				}}

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
					Status: []egressipv1.EgressIPStatus{},
				}

				fakeOvn.start(ctx,
					[]runtime.Object{
						&eIP,
					}, &v1.NodeList{
						Items: []v1.Node{node1, node2},
					})

				err := fakeOvn.controller.WatchEgressNodes()
				Expect(err).NotTo(HaveOccurred())
				err = fakeOvn.controller.WatchEgressIP()
				Expect(err).NotTo(HaveOccurred())

				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(0))
				Eventually(eIP.Status).Should(HaveLen(0))

				node1.Labels = map[string]string{
					"k8s.ovn.org/egress-assignable": "",
				}

				_, ip1V4Sub, err := net.ParseCIDR(node1IPv4)
				_, ip1V6Sub, err := net.ParseCIDR(node1IPv6)

				_, ip2V4Sub, err := net.ParseCIDR(node2IPv4)

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Update(context.TODO(), &node1, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(0))
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(1))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node1.Name))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v4Subnet).To(Equal(ip1V4Sub))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v6Subnet).To(Equal(ip1V6Sub))

				calculateCacheCount := func() int {
					cacheCount := 0
					fakeOvn.controller.egressAssignmentRetry.Range(func(key, value interface{}) bool {
						cacheCount++
						return true
					})
					return cacheCount
				}

				Eventually(calculateCacheCount).Should(Equal(1))

				node2.Labels = map[string]string{
					"k8s.ovn.org/egress-assignable": "",
				}

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Update(context.TODO(), &node2, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred())
				Eventually(status).Should(HaveLen(1))

				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.Name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveLen(2))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node1.Name))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node2.Name))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v4Subnet).To(Equal(ip1V4Sub))
				Expect(fakeOvn.controller.eIPAllocator[node1.Name].v6Subnet).To(Equal(ip1V6Sub))
				Expect(fakeOvn.controller.eIPAllocator[node2.Name].v4Subnet).To(Equal(ip2V4Sub))
				Eventually(calculateCacheCount).Should(Equal(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should re-balance EgressIPs when their node is removed", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP := "192.168.126.101"
				node1IPv4 := "192.168.126.12/24"
				node1IPv6 := "0:0:0:0:0:feff:c0a8:8e0c/64"
				node2IPv4 := "192.168.126.51/24"

				node1 := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: node1Name,
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": node1IPv4,
						"k8s.ovn.org/node-primary-cidr-ipv6": node1IPv6,
					},
					Labels: map[string]string{
						"k8s.ovn.org/egress-assignable": "",
					},
				}}
				node2 := v1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: node2Name,
					Annotations: map[string]string{
						"k8s.ovn.org/node-primary-cidr-ipv4": node2IPv4,
					},
					Labels: map[string]string{
						"k8s.ovn.org/egress-assignable": "",
					},
				}}

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
					Status: []egressipv1.EgressIPStatus{},
				}

				fakeOvn.start(ctx,
					[]runtime.Object{
						&eIP,
					}, &v1.NodeList{
						Items: []v1.Node{node1},
					})

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				err := fakeOvn.controller.WatchEgressNodes()
				Expect(err).NotTo(HaveOccurred())
				err = fakeOvn.controller.WatchEgressIP()
				Expect(err).NotTo(HaveOccurred())

				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(1))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node1.Name))
				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node1.Name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))

				_, err = fakeOvn.fakeClient.CoreV1().Nodes().Create(context.TODO(), &node2, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				Eventually(status).Should(HaveLen(1))
				statuses = status()
				Expect(statuses[0].Node).To(Equal(node1.Name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(2))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node1.Name))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node2.Name))

				err = fakeOvn.fakeClient.CoreV1().Nodes().Delete(context.TODO(), node1.Name, *metav1.NewDeleteOptions(0))
				Expect(err).NotTo(HaveOccurred())
				Eventually(fakeOvn.controller.eIPAllocator).Should(HaveLen(1))
				Expect(fakeOvn.controller.eIPAllocator).ToNot(HaveKey(node1.Name))
				Expect(fakeOvn.controller.eIPAllocator).To(HaveKey(node2.Name))
				Eventually(status).Should(HaveLen(1))
				statuses = status()
				Expect(statuses[0].Node).To(Equal(node2.Name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

	})

	Context("Dual-stack assignment", func() {

		It("should be able to allocate non-conflicting IPv4 on node which can host it, even if it happens to be the node with more assignments", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)
				egressIP := "192.168.126.99"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68", "192.168.126.102"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).NotTo(HaveOccurred())
				Expect(assignments).To(HaveLen(1))
				Expect(assignments[0].Node).To(Equal(node2.name))
				Expect(assignments[0].EgressIP).To(Equal(net.ParseIP(egressIP).String()))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

	})

	Context("IPv4 assignment", func() {

		It("Should not be able to assign egress IP defined in CIDR notation", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIPs := []string{"192.168.126.99/32"}

				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: egressIPs,
					},
				}

				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no matching host found"))
				Expect(assignments).To(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("IPv6 assignment", func() {

		It("should be able to allocate non-conflicting IP on node with lowest amount of allocations", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "0:0:0:0:0:feff:c0a8:8e0f"
				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).NotTo(HaveOccurred())
				Expect(assignments).To(HaveLen(1))
				Expect(assignments[0].Node).To(Equal(node2.name))
				Expect(assignments[0].EgressIP).To(Equal(net.ParseIP(egressIP).String()))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should be able to allocate several EgressIPs and avoid the same node", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP1 := "0:0:0:0:0:feff:c0a8:8e0d"
				egressIP2 := "0:0:0:0:0:feff:c0a8:8e0f"
				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1, egressIP2},
					},
				}
				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).NotTo(HaveOccurred())
				Expect(assignments).To(HaveLen(2))
				Expect(assignments[0].Node).To(Equal(node2.name))
				Expect(assignments[0].EgressIP).To(Equal(net.ParseIP(egressIP1).String()))
				Expect(assignments[1].Node).To(Equal(node1.name))
				Expect(assignments[1].EgressIP).To(Equal(net.ParseIP(egressIP2).String()))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should be able to allocate several EgressIPs and avoid the same node and leave one un-assigned without error", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP1 := "0:0:0:0:0:feff:c0a8:8e0d"
				egressIP2 := "0:0:0:0:0:feff:c0a8:8e0e"
				egressIP3 := "0:0:0:0:0:feff:c0a8:8e0f"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1, egressIP2, egressIP3},
					},
				}
				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).NotTo(HaveOccurred())
				Expect(assignments).To(HaveLen(2))
				Expect(assignments[0].Node).To(Equal(node2.name))
				Expect(assignments[0].EgressIP).To(Equal(net.ParseIP(egressIP1).String()))
				Expect(assignments[1].Node).To(Equal(node1.name))
				Expect(assignments[1].EgressIP).To(Equal(net.ParseIP(egressIP2).String()))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not be able to allocate already allocated IP", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "0:0:0:0:0:feff:c0a8:8e32"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{egressIP, "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				egressIPs := []string{egressIP}
				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: egressIPs,
					},
				}

				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no matching host found"))
				Expect(assignments).To(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should be able to allocate node IP", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "0:0:0:0:0:feff:c0a8:8e0c"

				node1 := setupNode(node1Name, []string{egressIP + "/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).NotTo(HaveOccurred())
				Expect(assignments).To(HaveLen(1))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not be able to allocate conflicting compressed IP", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "::feff:c0a8:8e32"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				egressIPs := []string{egressIP}

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: egressIPs,
					},
				}

				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no matching host found"))
				Expect(assignments).To(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not be able to allocate IPv4 IP on nodes which can only host IPv6", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "192.168.126.16"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIPs := []string{egressIP}
				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: eIPs,
					},
				}

				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no matching host found"))
				Expect(assignments).To(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should be able to allocate non-conflicting compressed uppercase IP", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "::FEFF:C0A8:8D32"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).NotTo(HaveOccurred())
				Expect(assignments).To(HaveLen(1))
				Expect(assignments[0].Node).To(Equal(node2.name))
				Expect(assignments[0].EgressIP).To(Equal(net.ParseIP(egressIP).String()))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not be able to allocate conflicting compressed uppercase IP", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIP := "::FEFF:C0A8:8E32"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2
				egressIPs := []string{egressIP}

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: egressIPs,
					},
				}

				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no matching host found"))
				Expect(assignments).To(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not be able to allocate invalid IP", func() {
			app.Action = func(ctx *cli.Context) error {

				fakeOvn.start(ctx, nil)

				egressIPs := []string{"0:0:0:0:0:feff:c0a8:8e32:5"}

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: egressIPs,
					},
				}

				assignments, err := fakeOvn.controller.assignEgressIPs(&eIP)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no matching host found"))
				Expect(assignments).To(HaveLen(0))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("WatchEgressIP", func() {

		It("should update status correctly for single-stack IPv4", func() {
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.start(ctx, nil)

				egressIP := "192.168.126.10"
				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
						NamespaceSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"name": "does-not-exist",
							},
						},
					},
				}
				fakeOvn.controller.WatchEgressIP()

				_, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update status correctly for single-stack IPv6", func() {
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.start(ctx, nil)

				egressIP := "0:0:0:0:0:feff:c0a8:8e0d"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e32", "0:0:0:0:0:feff:c0a8:8e1e"})
				node2 := setupNode(node2Name, []string{"0:0:0:0:0:fedf:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				fakeOvn.controller.WatchEgressIP()

				_, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(net.ParseIP(egressIP).String()))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update status correctly for dual-stack", func() {
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.start(ctx, nil)

				egressIPv4 := "192.168.126.101"
				egressIPv6 := "0:0:0:0:0:feff:c0a8:8e0d"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68", "192.168.126.102"})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIPv4, egressIPv6},
					},
				}
				fakeOvn.controller.WatchEgressIP()

				_, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(2))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(net.ParseIP(egressIPv4).String()))
				Expect(statuses[1].Node).To(Equal(node1.name))
				Expect(statuses[1].EgressIP).To(Equal(net.ParseIP(egressIPv6).String()))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("syncEgressIP for dual-stack", func() {

		It("should not update valid assignments", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIPv4 := "192.168.126.101"
				egressIPv6 := "0:0:0:0:0:feff:c0a8:8e0d"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68", "192.168.126.102"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIPv4, egressIPv6},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIPv4,
							Node:     node2.name,
						},
						{
							EgressIP: net.ParseIP(egressIPv6).String(),
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(2))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(eIP.Status[0].Node))
				Expect(statuses[0].EgressIP).To(Equal(eIP.Status[0].EgressIP))
				Expect(statuses[1].Node).To(Equal(eIP.Status[1].Node))
				Expect(statuses[1].EgressIP).To(Equal(eIP.Status[1].EgressIP))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update invalid assignments on UNKNOWN node", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIPv4 := "192.168.126.101"
				egressIPv6 := "0:0:0:0:0:feff:c0a8:8e0d"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68", "192.168.126.102"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIPv4, egressIPv6},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIPv4,
							Node:     "UNKNOWN",
						},
						{
							EgressIP: net.ParseIP(egressIPv6).String(),
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(2))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(eIP.Status[0].EgressIP))
				Expect(statuses[1].Node).To(Equal(eIP.Status[1].Node))
				Expect(statuses[1].EgressIP).To(Equal(eIP.Status[1].EgressIP))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update assignment on unsupported IP family node", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIPv4 := "192.168.126.101"

				node1 := setupNode(node1Name, []string{"0:0:0:0:0:feff:c0a8:8e0c/64"}, []string{"0:0:0:0:0:feff:c0a8:8e23"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68", "192.168.126.102"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIPv4},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIPv4,
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(eIP.Status[0].EgressIP))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("syncEgressIP for IPv4", func() {

		It("should update invalid assignments on duplicated node", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP1 := "192.168.126.101"
				egressIP2 := "192.168.126.100"

				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1, egressIP2},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIP1,
							Node:     node1.name,
						},
						{
							EgressIP: egressIP2,
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(2))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(eIP.Status[0].EgressIP))
				Expect(statuses[1].Node).To(Equal(node1.name))
				Expect(statuses[1].EgressIP).To(Equal(eIP.Status[1].EgressIP))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update invalid assignments with incorrectly parsed IP", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP1 := "192.168.126.101"
				egressIPIncorrect := "192.168.126.1000"

				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIPIncorrect,
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP1))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update invalid assignments with unhostable IP on a node", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP1 := "192.168.126.101"
				egressIPIncorrect := "192.168.128.100"

				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIPIncorrect,
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP1))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not update valid assignment", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP1 := "192.168.126.101"

				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1},
					},
					Status: []egressipv1.EgressIPStatus{
						{
							EgressIP: egressIP1,
							Node:     node1.name,
						},
					},
				}
				fakeOvn.start(ctx, []runtime.Object{
					&eIP,
				})

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node1.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP1))
				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("AddEgressIP for IPv4", func() {

		It("should not create two EgressIPs with same egress IP value", func() {
			app.Action = func(ctx *cli.Context) error {
				egressIP1 := "192.168.126.101"

				node1 := setupNode(node1Name, []string{"192.168.126.12/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP1 := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta("egressip"),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1},
					},
				}
				eIP2 := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta("egressip2"),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP1},
					},
				}
				fakeOvn.start(ctx, nil)

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				_, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP1, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP1.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP1))

				_, err = fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP2, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				status = func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP2.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(0))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create EgressIPs when request is node IP", func() {

			app.Action = func(ctx *cli.Context) error {

				egressIP := "192.168.126.12"

				node1 := setupNode(node1Name, []string{egressIP + "/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP1 := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIP),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				fakeOvn.start(ctx, nil)

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				_, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP1, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP1.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("UpdateEgressIP for IPv4", func() {

		It("should perform re-assingment of EgressIPs", func() {
			app.Action = func(ctx *cli.Context) error {

				egressIP := "192.168.126.101"
				updateEgressIP := "192.168.126.10"

				node1 := setupNode(node1Name, []string{egressIP + "/24"}, []string{"192.168.126.102", "192.168.126.111"})
				node2 := setupNode(node2Name, []string{"192.168.126.51/24"}, []string{"192.168.126.68"})

				eIP1 := egressipv1.EgressIP{
					ObjectMeta: newEgressIPMeta(egressIPName),
					Spec: egressipv1.EgressIPSpec{
						EgressIPs: []string{egressIP},
					},
				}
				fakeOvn.start(ctx, nil)

				fakeOvn.controller.eIPAllocator[node1.name] = &node1
				fakeOvn.controller.eIPAllocator[node2.name] = &node2

				fakeOvn.controller.WatchEgressIP()

				_, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Create(context.TODO(), &eIP1, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				status := func() []egressipv1.EgressIPStatus {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP1.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					return tmp.Status
				}

				Eventually(status).Should(HaveLen(1))
				statuses := status()
				Expect(statuses[0].Node).To(Equal(node2.name))
				Expect(statuses[0].EgressIP).To(Equal(egressIP))

				eIPToUpdate, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP1.Name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				eIPToUpdate.Spec.EgressIPs = []string{updateEgressIP}

				_, err = fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Update(context.TODO(), eIPToUpdate, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				newEgressIP := func() string {
					tmp, err := fakeOvn.fakeEgressClient.K8sV1().EgressIPs().Get(context.TODO(), eIP1.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(tmp.Status).Should(HaveLen(1))
					return tmp.Status[0].EgressIP
				}

				Eventually(newEgressIP).Should(Equal(updateEgressIP))
				statuses = status()
				Expect(statuses[0].Node).To(Equal(node2.name))

				return nil
			}

			err := app.Run([]string{app.Name})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
