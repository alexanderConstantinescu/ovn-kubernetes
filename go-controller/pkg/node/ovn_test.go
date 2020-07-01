package node

import (
	. "github.com/onsi/gomega"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	egressipfake "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressip/v1/apis/clientset/versioned/fake"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/urfave/cli/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
)

type FakeOVNNode struct {
	node             *OvnNode
	watcher          *factory.WatchFactory
	stopChan         chan struct{}
	recorder         *record.FakeRecorder
	fakeClient       *fake.Clientset
	fakeEgressClient *egressipfake.Clientset
	fakeExec         *ovntest.FakeExec
}

func NewFakeOVNNode(fexec *ovntest.FakeExec) *FakeOVNNode {
	err := util.SetExec(fexec)
	Expect(err).NotTo(HaveOccurred())

	return &FakeOVNNode{
		fakeExec: fexec,
		recorder: record.NewFakeRecorder(1),
	}
}

func (o *FakeOVNNode) start(ctx *cli.Context, egressObjects []runtime.Object, objects ...runtime.Object) {
	_, err := config.InitConfig(ctx, o.fakeExec, nil)
	Expect(err).NotTo(HaveOccurred())

	o.fakeClient = fake.NewSimpleClientset(objects...)
	o.fakeEgressClient = egressipfake.NewSimpleClientset(egressObjects...)
	o.init()
}

func (o *FakeOVNNode) restart() {
	o.shutdown()
	o.init()
}

func (o *FakeOVNNode) shutdown() {
	close(o.stopChan)
}

func (o *FakeOVNNode) init() {
	var err error

	o.stopChan = make(chan struct{})
	o.watcher, err = factory.NewWatchFactory(o.fakeClient, o.fakeEgressClient)
	Expect(err).NotTo(HaveOccurred())

	o.node = NewNode(o.fakeClient, o.watcher, "node", o.stopChan, o.recorder)
}
