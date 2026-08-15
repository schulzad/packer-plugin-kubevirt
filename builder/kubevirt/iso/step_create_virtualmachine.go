// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	kubevirtcommon "github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/common"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ptr "k8s.io/utils/ptr"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
)

type StepCreateVirtualMachine struct {
	Config Config
	Client kubecli.KubevirtClient
}

const (
	stateTemporaryVMCreated  = "temporary_vm_created"
	stateTemporaryVMDetached = "temporary_vm_detached"
)

func (s *StepCreateVirtualMachine) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace
	isoVolumeName, ok := state.Get(stateISOVolumeName).(string)
	if !ok || isoVolumeName == "" {
		err := fmt.Errorf("resolved ISO DataVolume name not found in build state")
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	diskSize := s.Config.DiskSize
	instanceTypeName := s.Config.InstanceType
	instanceTypeKind := s.Config.InstanceTypeKind
	preferenceName := s.Config.Preference
	preferenceKind := s.Config.PreferenceKind
	osType := s.Config.OperatingSystemType
	diskBus := s.Config.DiskBus
	diskInterface := s.Config.DiskInterface
	cpuSockets := s.Config.CPUSockets
	cpuCores := s.Config.CPUCores
	cpuThreads := s.Config.CPUThreads
	memory := s.Config.Memory
	networks := s.Config.Networks

	if osType == "" || (osType != "linux" && osType != "windows") {
		ui.Errorf("OS type of '%s' is not supported, set 'linux' or 'windows'.", osType)
		return multistep.ActionHalt
	}

	// KubeVirt masquerade forwards only explicitly declared ports to the guest,
	// so expose the active communicator's remote port. Without this the plugin's
	// port-forward can never reach WinRM/SSH inside the VM on a pod network.
	var forwardPorts []v1.Port
	switch s.Config.Communicator {
	case "winrm":
		port := s.Config.WinRMRemotePort
		if port == 0 {
			port = 5985
		}
		forwardPorts = []v1.Port{{Name: "winrm", Port: int32(port), Protocol: "TCP"}}
	case "ssh":
		port := s.Config.SSHRemotePort
		if port == 0 {
			port = 22
		}
		forwardPorts = []v1.Port{{Name: "ssh", Port: int32(port), Protocol: "TCP"}}
	}

	virtualMachine := virtualMachine(
		name,
		isoVolumeName,
		diskSize,
		instanceTypeName,
		preferenceName,
		instanceTypeKind,
		preferenceKind,
		osType,
		diskBus,
		diskInterface,
		cpuSockets,
		cpuCores,
		cpuThreads,
		memory,
		s.Config.ShutdownCommand,
		networks,
		forwardPorts)

	extraMedia := extraMediaAttachments(s.Config.ExtraMedia)
	extraVolumes, extraDisks := kubevirtcommon.ExtraMediaDevices(extraMedia)
	virtualMachine.Spec.Template.Spec.Domain.Devices.Disks = append(virtualMachine.Spec.Template.Spec.Domain.Devices.Disks, extraDisks...)
	virtualMachine.Spec.Template.Spec.Volumes = append(virtualMachine.Spec.Template.Spec.Volumes, extraVolumes...)

	if err := kubevirtcommon.PreflightExtraMedia(ctx, s.Client, namespace, extraMedia, ui.Sayf); err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	ui.Sayf("Creating a new temporary VirtualMachine (%s/%s)...", namespace, name)

	_, err := s.Client.VirtualMachine(namespace).Create(ctx, virtualMachine, metav1.CreateOptions{})
	if err != nil {
		_, getErr := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr == nil || !apierrors.IsNotFound(getErr) {
			// An existing VM, or an ambiguous API result, may reference the
			// staged ISO. Preserve it unless absence is confirmed.
			state.Put(stateTemporaryVMCreated, true)
		}
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	state.Put(stateTemporaryVMCreated, true)

	if err := s.waitUntilVirtualMachineReady(ctx); err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	return multistep.ActionContinue
}

func (s *StepCreateVirtualMachine) Cleanup(state multistep.StateBag) {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace
	keepVM := s.Config.KeepVM

	if keepVM {
		ui.Sayf("Keeping VirtualMachine (%s/%s).", namespace, name)
		return
	}

	ui.Sayf("Deleting VirtualMachine (%s/%s)...", namespace, name)

	deleteErr := s.Client.VirtualMachine(namespace).Delete(context.Background(), name, metav1.DeleteOptions{
		GracePeriodSeconds: ptr.To(int64(0)),
	})
	if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
		ui.Errorf("Failed to delete VirtualMachine %s/%s: %v", namespace, name, deleteErr)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, vmErr := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
		_, vmiErr := s.Client.VirtualMachineInstance(namespace).Get(ctx, name, metav1.GetOptions{})
		vmGone := apierrors.IsNotFound(vmErr)
		vmiGone := apierrors.IsNotFound(vmiErr)
		if vmErr != nil && !vmGone {
			return false, vmErr
		}
		if vmiErr != nil && !vmiGone {
			return false, vmiErr
		}
		return vmGone && vmiGone, nil
	}); err != nil {
		ui.Errorf("Timed out waiting for VirtualMachine %s/%s storage to detach: %v", namespace, name, err)
		return
	}
	state.Put(stateTemporaryVMDetached, true)
}

const (
	virtualMachineReadyPollInterval = 5 * time.Second
	virtualMachineReadyPollTimeout  = time.Hour
)

var volumeAttachFailureEventReasons = map[string]struct{}{
	"FailedAttachVolume": {},
	"FailedMount":        {},
	"FailedMapVolume":    {},
}

// waitUntilVirtualMachineReady polls until the VM reports Ready, the timeout
// elapses, or the build is interrupted. It never aborts the wait on its own when
// it observes an unhealthy state (volume-attach failures, unschedulable/error
// PrintableStatus, a Failed VMI phase, etc.): those conditions are frequently
// transient on some storage backends (e.g. Longhorn retrying an attach), so the
// decision to give up is left to the timeout or the operator. Instead, it emits
// rich diagnostics (status, conditions, warning/attach events) which Packer only
// surfaces when PACKER_LOG is set, turning what used to be a silent hang into an
// explainable one.
func (s *StepCreateVirtualMachine) waitUntilVirtualMachineReady(ctx context.Context) error {
	name := s.Config.Name
	namespace := s.Config.Namespace

	var lastReport string
	var lastReportedAt time.Time

	err := wait.PollUntilContextTimeout(ctx, virtualMachineReadyPollInterval, virtualMachineReadyPollTimeout, true, func(ctx context.Context) (bool, error) {
		vm, err := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if vm.Status.Ready {
			return true, nil
		}

		vmi, vmiErr := s.Client.VirtualMachineInstance(namespace).Get(ctx, name, metav1.GetOptions{})
		if vmiErr != nil {
			if !apierrors.IsNotFound(vmiErr) {
				return false, vmiErr
			}
			vmi = nil
		}
		events, eventsErr := s.listVirtualMachineWarningEvents(ctx, namespace, name)
		if eventsErr != nil {
			// Diagnostics are best-effort; never fail the wait because events
			// could not be read.
			log.Printf("[DEBUG] Could not list warning events for VirtualMachine %s/%s: %v", namespace, name, eventsErr)
			events = nil
		}

		report := virtualMachineWaitDiagnostics(vm, vmi, events)
		if report != lastReport || time.Since(lastReportedAt) >= time.Minute {
			// Packer only surfaces plugin log.Printf output when PACKER_LOG is set.
			log.Printf("[DEBUG] Waiting for VirtualMachine %s/%s: %s", namespace, name, report)
			lastReport = report
			lastReportedAt = time.Now()
		}
		return false, nil
	})
	if err == nil {
		return nil
	}
	// The wait ended without the VM becoming Ready (timeout, interruption, or an
	// API error). Attach the latest diagnostics so the failure explains itself.
	vm, _ := s.Client.VirtualMachine(namespace).Get(context.Background(), name, metav1.GetOptions{})
	vmi, _ := s.Client.VirtualMachineInstance(namespace).Get(context.Background(), name, metav1.GetOptions{})
	events, _ := s.listVirtualMachineWarningEvents(context.Background(), namespace, name)
	return fmt.Errorf(
		"wait for VirtualMachine %s/%s to become Ready: %w (%s)",
		namespace, name, err, virtualMachineWaitDiagnostics(vm, vmi, events),
	)
}

func (s *StepCreateVirtualMachine) listVirtualMachineWarningEvents(ctx context.Context, namespace, name string) ([]corev1.Event, error) {
	var events []corev1.Event

	for _, kind := range []string{"VirtualMachine", "VirtualMachineInstance"} {
		list, err := s.Client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.kind=%s,involvedObject.name=%s,type=%s", kind, name, corev1.EventTypeWarning),
		})
		if err != nil {
			return nil, err
		}
		events = append(events, list.Items...)
	}

	pods, err := s.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", v1.VirtualMachineNameLabel, name),
	})
	if err != nil {
		return nil, err
	}
	for _, pod := range pods.Items {
		list, err := s.Client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.kind=Pod,involvedObject.name=%s,type=%s", pod.Name, corev1.EventTypeWarning),
		})
		if err != nil {
			return nil, err
		}
		events = append(events, list.Items...)
	}
	return events, nil
}

func virtualMachineWaitReport(vm *v1.VirtualMachine) string {
	if vm == nil {
		return "status=unknown"
	}
	report := fmt.Sprintf("printableStatus=%s ready=%t", vm.Status.PrintableStatus, vm.Status.Ready)
	if msg := conditionMessages(vm.Status.Conditions); msg != "" {
		report += " conditions=[" + msg + "]"
	}
	return report
}

func appendVirtualMachineInstanceWaitReport(report string, vmi *v1.VirtualMachineInstance) string {
	if vmi == nil {
		return report
	}
	report += fmt.Sprintf(" vmiPhase=%s", vmi.Status.Phase)
	if msg := instanceConditionMessages(vmi.Status.Conditions); msg != "" {
		report += " vmiConditions=[" + msg + "]"
	}
	return report
}

func virtualMachineWaitDiagnostics(vm *v1.VirtualMachine, vmi *v1.VirtualMachineInstance, events []corev1.Event) string {
	parts := []string{virtualMachineWaitReport(vm)}
	if vmi != nil {
		parts[0] = appendVirtualMachineInstanceWaitReport(parts[0], vmi)
	}
	if msg := volumeAttachFailureMessage(events); msg != "" {
		parts = append(parts, "volumeEvent="+msg)
	} else if msg := recentWarningEventMessage(events); msg != "" {
		parts = append(parts, "warningEvent="+msg)
	}
	return strings.Join(parts, " ")
}

func conditionMessages(conditions []v1.VirtualMachineCondition) string {
	var parts []string
	for _, condition := range conditions {
		interesting := false
		switch condition.Type {
		case v1.VirtualMachineFailure:
			interesting = condition.Status == corev1.ConditionTrue
		case v1.VirtualMachineReady:
			interesting = condition.Status != corev1.ConditionTrue
		}
		if !interesting || (condition.Message == "" && condition.Reason == "") {
			continue
		}
		parts = append(parts, formatCondition(string(condition.Type), condition.Reason, condition.Message))
	}
	return strings.Join(parts, "; ")
}

func instanceConditionMessages(conditions []v1.VirtualMachineInstanceCondition) string {
	var parts []string
	for _, condition := range conditions {
		interesting := false
		switch condition.Type {
		case v1.VirtualMachineInstanceReady,
			v1.VirtualMachineInstanceSynchronized,
			v1.VirtualMachineInstanceDataVolumesReady:
			interesting = condition.Status != corev1.ConditionTrue
		}
		if !interesting || (condition.Message == "" && condition.Reason == "") {
			continue
		}
		parts = append(parts, formatCondition(string(condition.Type), condition.Reason, condition.Message))
	}
	return strings.Join(parts, "; ")
}

func formatCondition(condType, reason, message string) string {
	switch {
	case reason != "" && message != "":
		return fmt.Sprintf("%s=%s: %s", condType, reason, message)
	case reason != "":
		return fmt.Sprintf("%s=%s", condType, reason)
	default:
		return fmt.Sprintf("%s=%s", condType, message)
	}
}

func volumeAttachFailureMessage(events []corev1.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if _, ok := volumeAttachFailureEventReasons[event.Reason]; ok {
			return formatEvent(event)
		}
	}
	return ""
}

func recentWarningEventMessage(events []corev1.Event) string {
	if len(events) == 0 {
		return ""
	}
	return formatEvent(events[len(events)-1])
}

func formatEvent(event corev1.Event) string {
	if event.Message == "" {
		return event.Reason
	}
	return fmt.Sprintf("%s: %s", event.Reason, event.Message)
}
