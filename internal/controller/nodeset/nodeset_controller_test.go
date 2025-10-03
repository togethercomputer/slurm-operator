// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeset

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/set"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/fake"
	"github.com/SlinkyProject/slurm-client/pkg/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"

	slinkyv1alpha1 "github.com/togethercomputer/slurm-operator/api/v1alpha1"
)

func newFakeClientList(interceptorFuncs interceptor.Funcs, initObjLists ...object.ObjectList) slurmclient.Client {
	return fake.NewClientBuilder().
		WithLists(initObjLists...).
		WithInterceptorFuncs(interceptorFuncs).
		Build()
}

var _ = Describe("Nodeset controller", func() {

	const (
		nodesetName      = "test-nodeset"
		nodesetNamespace = "default"
		clusterName      = "test-cluster"

		timeout  = time.Second * 30
		duration = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating a NodeSet", func() {
		It("Should successfully create create a pod for the node", func() {

			ctx := context.Background()

			// Create a slurmClient with cached nodes before
			// creating the nodeset so the reconcile loop
			// will create pods on the matching nodes.
			slurmClusters.Add(types.NamespacedName{Name: clusterName, Namespace: nodesetNamespace},
				newFakeClientList(interceptor.Funcs{}, &slurmtypes.NodeList{
					Items: []slurmtypes.Node{
						{Name: "node-1", State: make(set.Set[slurmtypes.NodeState], 0)},
						{Name: "node-2", State: make(set.Set[slurmtypes.NodeState], 0)},
					},
				}))

			// Initialize K8s nodes so the NodeSet Controller
			// places pod(s) on the nodes that fit
			nodes := []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
					},
				},
			}
			for _, node := range nodes {
				Expect(k8sClient.Create(ctx, &node)).To(Succeed())
			}

			By("By creating a new Nodeset")
			nodeset := &slinkyv1alpha1.NodeSet{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "slinky.slurm.net/v1alpha1",
					Kind:       "NodeSet",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodesetName,
					Namespace: nodesetNamespace,
				},
				Spec: slinkyv1alpha1.NodeSetSpec{
					ClusterName: clusterName,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod",
							Namespace:   nodesetNamespace,
							Labels:      map[string]string{"foo": "bar"},
							Annotations: map[string]string{"biz": "buz"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "image-foo",
								},
							},
							Tolerations: []corev1.Toleration{
								{
									// Tolerate this taint when running
									// in test mode as manually added nodes
									// will automatically be tainted
									Key:    "node.kubernetes.io/not-ready",
									Effect: corev1.TaintEffectNoSchedule,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodeset)).To(Succeed())

			nodesetLookupKey := types.NamespacedName{Name: nodesetName, Namespace: nodesetNamespace}
			createdNodeset := &slinkyv1alpha1.NodeSet{}

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, nodesetLookupKey, createdNodeset)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Expect(createdNodeset.Spec.ClusterName).To(Equal("test-cluster"))

			// Wait for two pods to be created by the NodeSet Controller
			podList := &corev1.PodList{}
			optsList := &k8sclient.ListOptions{
				Namespace:     nodeset.Namespace,
				LabelSelector: labels.Everything(),
			}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, podList, optsList)).To(Succeed())
				g.Expect(len(podList.Items)).To(Equal(len(nodes)))
			}, timeout, interval).Should(Succeed())

			// Scale down a NodeSet to verify pods are deleted and
			// Slurm nodes are drained and deleted
			replicas := new(int32)
			*replicas = 0
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, nodesetLookupKey, createdNodeset)).To(Succeed())
			}, timeout, interval).Should(Succeed())
			createdNodeset.Spec.Replicas = replicas
			Expect(k8sClient.Update(ctx, createdNodeset)).To(Succeed())

			// Verify the Slurm nodes are marked as NodeStateDRAIN
			drainState := make(set.Set[slurmtypes.NodeState])
			drainState.Insert(slurmtypes.NodeStateDRAIN)
			Eventually(func(g Gomega) {
				slurmNodes := &slurmtypes.NodeList{}
				g.Expect(slurmClusters.Get(types.NamespacedName{Namespace: nodesetNamespace, Name: clusterName}).List(ctx, slurmNodes)).To(Succeed())
				for _, node := range slurmNodes.Items {
					g.Expect(node.State.Equal(drainState)).Should(BeTrue())
				}
			}, timeout, interval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, createdNodeset)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, nodesetLookupKey, createdNodeset)).ShouldNot(Succeed())
			}, timeout, interval).Should(Succeed())
		})
	})
})
