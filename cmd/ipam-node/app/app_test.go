/*
 Copyright 2023, NVIDIA CORPORATION & AFFILIATES
 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at
     http://www.apache.org/licenses/LICENSE-2.0
 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	nodev1 "github.com/Mellanox/nvidia-k8s-ipam/api/grpc/nvidia/ipam/node/v1"
	ipamv1alpha1 "github.com/Mellanox/nvidia-k8s-ipam/api/v1alpha1"
	"github.com/Mellanox/nvidia-k8s-ipam/cmd/ipam-node/app"
	"github.com/Mellanox/nvidia-k8s-ipam/cmd/ipam-node/app/options"
	"github.com/Mellanox/nvidia-k8s-ipam/pkg/cni/types"
)

const (
	testNodeName  = "test-node"
	testPodName   = "test-pod"
	testPoolName1 = "my-pool-1"
	testPoolName2 = "my-pool-2"
	testNamespace = "default"
)

func createTestIPPools() {
	pool1 := &ipamv1alpha1.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName1, Namespace: testNamespace},
		Spec: ipamv1alpha1.IPPoolSpec{
			Subnet:           "192.168.0.0/16",
			PerNodeBlockSize: 252,
			Gateway:          "192.168.0.1",
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pool1))
	pool1.Status = ipamv1alpha1.IPPoolStatus{
		Allocations: []ipamv1alpha1.Allocation{
			{
				NodeName: testNodeName,
				StartIP:  "192.168.0.2",
				EndIP:    "192.168.0.254",
			},
		}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pool1))

	pool2 := &ipamv1alpha1.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName2, Namespace: testNamespace},
		Spec: ipamv1alpha1.IPPoolSpec{
			Subnet:           "10.100.0.0/16",
			PerNodeBlockSize: 252,
			Gateway:          "10.100.0.1",
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pool2))
	pool2.Status = ipamv1alpha1.IPPoolStatus{
		Allocations: []ipamv1alpha1.Allocation{
			{
				NodeName: testNodeName,
				StartIP:  "10.100.0.2",
				EndIP:    "10.100.0.254",
			},
		}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pool2))
}

func createTestCIDRPools() {
	pool1 := &ipamv1alpha1.CIDRPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName1, Namespace: testNamespace},
		Spec: ipamv1alpha1.CIDRPoolSpec{
			CIDR:                 "192.100.0.0/16",
			GatewayIndex:         ptr.To[int32](1),
			PerNodeNetworkPrefix: 24,
			Exclusions: []ipamv1alpha1.ExcludeRange{
				{StartIP: "192.100.0.1", EndIP: "192.100.0.10"},
			},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pool1))
	pool1.Status = ipamv1alpha1.CIDRPoolStatus{
		Allocations: []ipamv1alpha1.CIDRPoolAllocation{
			{
				NodeName: testNodeName,
				Prefix:   "192.100.0.0/24",
				Gateway:  "192.100.0.1",
			},
		}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pool1))

	pool2 := &ipamv1alpha1.CIDRPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName2, Namespace: testNamespace},
		Spec: ipamv1alpha1.CIDRPoolSpec{
			CIDR:                 "10.200.0.0/24",
			PerNodeNetworkPrefix: 31,
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pool2))
	pool2.Status = ipamv1alpha1.CIDRPoolStatus{
		Allocations: []ipamv1alpha1.CIDRPoolAllocation{
			{
				NodeName: testNodeName,
				Prefix:   "10.200.0.0/31",
			},
		}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pool2))
}

func createTestPod() *corev1.Pod {
	podObj := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: testNamespace},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "name", Image: "image"}},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, podObj))
	return podObj
}

func getOptions(testDir string) *options.Options {
	daemonSocket := "unix://" + filepath.Join(testDir, "daemon")
	storePath := filepath.Join(testDir, "store")
	cniBinDir := filepath.Join(testDir, "cnibin")
	cniConfDir := filepath.Join(testDir, "cniconf")
	dummyCNIBin := filepath.Join(testDir, "dummycni")

	Expect(os.WriteFile(dummyCNIBin, []byte("dummy"), 0777)).NotTo(HaveOccurred())
	Expect(os.Mkdir(cniBinDir, 0777)).NotTo(HaveOccurred())
	Expect(os.Mkdir(cniConfDir, 0777)).NotTo(HaveOccurred())

	opts := options.New()
	opts.NodeName = testNodeName
	opts.ProbeAddr = "0"   // disable
	opts.MetricsAddr = "0" // disable
	opts.BindAddress = daemonSocket
	opts.StoreFile = storePath
	opts.CNIBinFile = dummyCNIBin
	opts.CNIBinDir = cniBinDir
	opts.CNIConfDir = cniConfDir
	opts.CNIDaemonSocket = daemonSocket
	opts.PoolsNamespace = testNamespace
	opts.CNIForcePoolName = true
	return opts
}

func getValidReqParams(uid, name, namespace string) *nodev1.IPAMParameters {
	return &nodev1.IPAMParameters{
		Pools:          []string{testPoolName1, testPoolName2},
		CniContainerid: "id1",
		CniIfname:      "net0",
		Metadata: &nodev1.IPAMMetadata{
			K8SPodName:      name,
			K8SPodNamespace: namespace,
			K8SPodUid:       uid,
			DeviceId:        "0000:d8:00.1",
		},
	}
}

func pathExists(path string) error {
	_, err := os.Stat(path)
	return err
}

var _ = Describe("IPAM Node daemon", func() {
	var (
		wg          sync.WaitGroup
		testDir     string
		opts        *options.Options
		cFuncDaemon context.CancelFunc
		daemonCtx   context.Context
	)

	BeforeEach(func() {
		wg = sync.WaitGroup{}
		testDir = GinkgoT().TempDir()
		opts = getOptions(testDir)

		daemonCtx, cFuncDaemon = context.WithCancel(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer GinkgoRecover()
			Expect(app.RunNodeDaemon(logr.NewContext(daemonCtx, klog.NewKlogr()), cfg, opts)).NotTo(HaveOccurred())
		}()
	})

	AfterEach(func() {
		cFuncDaemon()
		wg.Wait()
	})

	It("Validate main flows", func() {
		createTestIPPools()
		createTestCIDRPools()
		pod := createTestPod()

		conn, err := grpc.NewClient(opts.CNIDaemonSocket, grpc.WithTransportCredentials(insecure.NewCredentials()))
		Expect(err).NotTo(HaveOccurred())

		grpcClient := nodev1.NewIPAMServiceClient(conn)

		cidrPoolParams := getValidReqParams(string(pod.UID), pod.Name, pod.Namespace)
		cidrPoolParams.PoolType = nodev1.PoolType_POOL_TYPE_CIDRPOOL
		ipPoolParams := getValidReqParams(string(pod.UID), pod.Name, pod.Namespace)

		for _, params := range []*nodev1.IPAMParameters{ipPoolParams, cidrPoolParams} {
			// no allocation yet
			_, err = grpcClient.IsAllocated(ctx,
				&nodev1.IsAllocatedRequest{Parameters: params})
			Expect(status.Code(err) == codes.NotFound).To(BeTrue())

			// allocate
			resp, err := grpcClient.Allocate(ctx, &nodev1.AllocateRequest{Parameters: params})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Allocations).To(HaveLen(2))
			Expect(resp.Allocations[0].Pool).NotTo(BeEmpty())
			Expect(resp.Allocations[0].Gateway).NotTo(BeEmpty())
			Expect(resp.Allocations[0].Ip).NotTo(BeEmpty())

			_, err = grpcClient.IsAllocated(ctx,
				&nodev1.IsAllocatedRequest{Parameters: params})
			Expect(err).NotTo(HaveOccurred())

			// deallocate
			_, err = grpcClient.Deallocate(ctx, &nodev1.DeallocateRequest{Parameters: params})
			Expect(err).NotTo(HaveOccurred())

			// deallocate should be idempotent
			_, err = grpcClient.Deallocate(ctx, &nodev1.DeallocateRequest{Parameters: params})
			Expect(err).NotTo(HaveOccurred())

			// check should fail
			_, err = grpcClient.IsAllocated(ctx,
				&nodev1.IsAllocatedRequest{Parameters: params})
			Expect(status.Code(err) == codes.NotFound).To(BeTrue())
		}
	})

	It("deployShimCNI works as expected", func() {
		// cni binary copied
		Eventually(func() error {
			return pathExists(filepath.Join(testDir, "cnibin", "nv-ipam"))
		}).
			WithTimeout(2 * time.Second).
			ShouldNot(HaveOccurred())
		// conf file copied
		Eventually(func() error {
			return pathExists(filepath.Join(testDir, "cniconf", "nv-ipam.conf"))
		}).
			WithTimeout(2 * time.Second).
			ShouldNot(HaveOccurred())
		// store dir created
		Eventually(func() error {
			return pathExists(filepath.Join(testDir, "store"))
		}).
			WithTimeout(2 * time.Second).
			ShouldNot(HaveOccurred())
		// conf file contains expected results
		data, err := os.ReadFile(filepath.Join(testDir, "cniconf", "nv-ipam.conf"))
		Expect(err).ToNot(HaveOccurred())
		ipamConf := types.IPAMConf{}
		Expect(json.Unmarshal(data, &ipamConf)).ToNot(HaveOccurred())
		Expect(ipamConf.DaemonSocket).To(Equal(opts.CNIDaemonSocket))
		Expect(ipamConf.DaemonCallTimeoutSeconds).To(Equal(opts.CNIDaemonCallTimeoutSeconds))
		Expect(ipamConf.LogFile).To(Equal(opts.CNILogFile))
		Expect(ipamConf.LogLevel).To(Equal(opts.CNILogLevel))
		Expect(ipamConf.ForcePoolName).ToNot(BeNil())
		Expect(*ipamConf.ForcePoolName).To(Equal(opts.CNIForcePoolName))
	})
})
