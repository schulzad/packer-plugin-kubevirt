// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso_test

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	v1 "kubevirt.io/api/core/v1"
	fakecdiclient "kubevirt.io/client-go/containerizeddataimporter/fake"
	"kubevirt.io/client-go/kubecli"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
)

var _ = Describe("StepCreateVirtualMachine", func() {
	const (
		namespace = "test-ns"
		name      = "test-vm"
	)

	var (
		ctrl       *gomock.Controller
		kubeClient *fakek8sclient.Clientset
		cdiClient  *fakecdiclient.Clientset
		virtClient kubecli.KubevirtClient
		vmClient   *kubevirtfake.Clientset
		state      *multistep.BasicStateBag
		step       *iso.StepCreateVirtualMachine
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())

		uiErr := &strings.Builder{}
		ui := &packer.BasicUi{
			Reader:      strings.NewReader(""),
			Writer:      io.Discard,
			ErrorWriter: uiErr,
		}
		state = new(multistep.BasicStateBag)
		state.Put("ui", ui)
		state.Put("iso_volume_name", "resolved-iso-vol")

		kubeClient = fakek8sclient.NewSimpleClientset()
		cdiClient = fakecdiclient.NewSimpleClientset()
		vmClient = kubevirtfake.NewSimpleClientset()

		kubecli.GetKubevirtClientFromClientConfig = kubecli.GetMockKubevirtClientFromClientConfig
		kubecli.MockKubevirtClientInstance = kubecli.NewMockKubevirtClient(ctrl)
		kubecli.MockKubevirtClientInstance.EXPECT().CoreV1().Return(kubeClient.CoreV1()).AnyTimes()
		kubecli.MockKubevirtClientInstance.EXPECT().
			VirtualMachine(gomock.Any()).
			DoAndReturn(func(ns string) kubecli.VirtualMachineInterface {
				return vmClient.KubevirtV1().VirtualMachines(ns)
			}).AnyTimes()
		kubecli.MockKubevirtClientInstance.EXPECT().
			VirtualMachineInstance(gomock.Any()).
			DoAndReturn(func(ns string) kubecli.VirtualMachineInstanceInterface {
				return vmClient.KubevirtV1().VirtualMachineInstances(ns)
			}).AnyTimes()
		kubecli.MockKubevirtClientInstance.EXPECT().CdiClient().Return(cdiClient).AnyTimes()

		virtClient, _ = kubecli.GetKubevirtClientFromClientConfig(nil)

		step = &iso.StepCreateVirtualMachine{
			Config: iso.Config{
				Name:                name,
				Namespace:           namespace,
				IsoVolumeName:       "configured-iso-vol",
				DiskSize:            "1Gi",
				InstanceType:        "cx1.medium",
				InstanceTypeKind:    "instancetype.kubevirt.io",
				Preference:          "fedora",
				PreferenceKind:      "instancetype.kubevirt.io",
				OperatingSystemType: "linux",
				KeepVM:              false,
			},
			Client: virtClient,
		}
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Context("Run", func() {
		It("halts when OS type is unsupported", func() {
			step.Config.OperatingSystemType = "bsd"
			action := step.Run(context.Background(), state)
			Expect(action).To(Equal(multistep.ActionHalt))
		})

		It("continues when VM is created and becomes Ready", func() {
			// Let Run create the VM, then mark it Ready
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Watch for VM creation and patch Ready status
			vmClient.Fake.PrependReactor("create", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
				create := action.(k8stesting.CreateAction)
				obj := create.GetObject().(*v1.VirtualMachine)
				foundCDROM := false
				for _, volume := range obj.Spec.Template.Spec.Volumes {
					if volume.Name == "cdrom" {
						foundCDROM = true
						Expect(volume.DataVolume).NotTo(BeNil())
						Expect(volume.DataVolume.Name).To(Equal("resolved-iso-vol"))
					}
				}
				Expect(foundCDROM).To(BeTrue())
				// Simulate that VM is created and becomes Ready
				obj.Status.Ready = true
				return false, obj, nil
			})

			action := step.Run(ctx, state)
			Expect(action).To(Equal(multistep.ActionContinue))
		})

		It("sets the configured root disk interface on a Windows VM", func() {
			step.Config.OperatingSystemType = "windows"
			step.Config.DiskInterface = "virtio"

			vmClient.Fake.PrependReactor("create", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
				create := action.(k8stesting.CreateAction)
				obj := create.GetObject().(*v1.VirtualMachine)

				var rootDisk *v1.Disk
				for i := range obj.Spec.Template.Spec.Domain.Devices.Disks {
					if obj.Spec.Template.Spec.Domain.Devices.Disks[i].Name == "rootdisk" {
						rootDisk = &obj.Spec.Template.Spec.Domain.Devices.Disks[i]
						break
					}
				}
				Expect(rootDisk).NotTo(BeNil())
				Expect(rootDisk.DiskDevice.Disk).NotTo(BeNil())
				Expect(rootDisk.DiskDevice.Disk.Bus).To(Equal(v1.DiskBusVirtio))

				obj.Status.Ready = true
				return false, obj, nil
			})

			action := step.Run(context.Background(), state)
			Expect(action).To(Equal(multistep.ActionContinue))
		})

		It("halts when VM creation fails", func() {
			// Inject error into fake client
			vmClient.Fake.PrependReactor("create", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("simulated create error")
			})

			action := step.Run(context.Background(), state)
			Expect(action).To(Equal(multistep.ActionHalt))
		})

		It("halts when VM never becomes Ready", func() {
			_, err := vmClient.KubevirtV1().VirtualMachines(namespace).Create(context.Background(),
				&v1.VirtualMachine{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: namespace,
					},
					Status: v1.VirtualMachineStatus{Ready: false},
				},
				metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			action := step.Run(ctx, state)
			Expect(action).To(Equal(multistep.ActionHalt))
			Expect(state.Get("temporary_vm_created")).To(BeTrue())
		})

		It("surfaces error PrintableStatus in diagnostics without aborting early", func() {
			vmClient.Fake.PrependReactor("create", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
				create := action.(k8stesting.CreateAction)
				obj := create.GetObject().(*v1.VirtualMachine)
				obj.Status.Ready = false
				obj.Status.PrintableStatus = v1.VirtualMachineStatusUnschedulable
				obj.Status.Conditions = []v1.VirtualMachineCondition{{
					Type:    v1.VirtualMachineFailure,
					Status:  corev1.ConditionTrue,
					Reason:  "Unschedulable",
					Message: "0/3 nodes are available",
				}}
				return false, obj, nil
			})

			// A cancelled context stands in for the poll timeout so the test does
			// not have to wait for the VM to become Ready.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			action := step.Run(ctx, state)
			Expect(action).To(Equal(multistep.ActionHalt))
			err, ok := state.Get("error").(error)
			Expect(ok).To(BeTrue())
			// Diagnostics are surfaced for visibility...
			Expect(err.Error()).To(ContainSubstring("ErrorUnschedulable"))
			Expect(err.Error()).To(ContainSubstring("0/3 nodes are available"))
			// ...but the wait never self-aborts on the status; it ends via the
			// context/timeout, not a plugin decision.
			Expect(err.Error()).To(ContainSubstring("to become Ready"))
			Expect(err.Error()).NotTo(ContainSubstring("entered error state"))
		})

		It("surfaces FailedAttachVolume in diagnostics without aborting early", func() {
			vmClient.Fake.PrependReactor("create", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
				create := action.(k8stesting.CreateAction)
				obj := create.GetObject().(*v1.VirtualMachine)
				obj.Status.Ready = false
				obj.Status.PrintableStatus = v1.VirtualMachineStatusStarting
				return false, obj, nil
			})

			_, err := kubeClient.CoreV1().Events(namespace).Create(context.Background(), &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + ".attach-fail",
					Namespace: namespace,
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "VirtualMachineInstance",
					Namespace: namespace,
					Name:      name,
				},
				Reason:  "FailedAttachVolume",
				Message: "AttachVolume.Attach failed for volume pvc-iso: volume is not ready for workloads",
				Type:    corev1.EventTypeWarning,
			}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			action := step.Run(ctx, state)
			Expect(action).To(Equal(multistep.ActionHalt))
			waitErr, ok := state.Get("error").(error)
			Expect(ok).To(BeTrue())
			// The attach failure is surfaced for visibility...
			Expect(waitErr.Error()).To(ContainSubstring("FailedAttachVolume"))
			Expect(waitErr.Error()).To(ContainSubstring("volume is not ready for workloads"))
			// ...but the plugin does not decide to fail on it; the wait ends via
			// the context/timeout, not a 2-minute self-abort.
			Expect(waitErr.Error()).To(ContainSubstring("to become Ready"))
			Expect(waitErr.Error()).NotTo(ContainSubstring("volume attach failed"))
		})
	})

	Context("Cleanup", func() {
		It("keeps VM when KeepVM is true", func() {
			step.Config.KeepVM = true
			step.Cleanup(state) // should not panic
		})

		It("deletes VM when KeepVM is false", func() {
			_, err := vmClient.KubevirtV1().VirtualMachines(namespace).Create(context.Background(),
				&v1.VirtualMachine{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: namespace,
					},
				},
				metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			step.Cleanup(state)

			_, err = vmClient.KubevirtV1().VirtualMachines(namespace).Get(context.Background(), name, metav1.GetOptions{})
			Expect(err).To(HaveOccurred()) // deleted
			Expect(state.Get("temporary_vm_detached")).To(BeTrue())
		})
	})
})
