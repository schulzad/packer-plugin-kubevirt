// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package common

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
)

const defaultShutdownPollInterval = 5 * time.Second

// ShutdownClient is the subset of the KubeVirt client used while waiting for a
// guest-initiated shutdown.
type ShutdownClient interface {
	VirtualMachine(namespace string) kubecli.VirtualMachineInterface
	VirtualMachineInstance(namespace string) kubecli.VirtualMachineInstanceInterface
}

// StepShutdown executes an opaque shutdown command through Packer's active
// communicator, then waits for the guest to power itself off. It intentionally
// knows nothing about the guest OS or what preparation the command performs.
//
// An empty ShutdownCommand is a no-op; the builder's existing API-stop step
// remains responsible for stopping the VM.
type StepShutdown struct {
	Client          ShutdownClient
	Name            string
	Namespace       string
	ShutdownCommand string
	ShutdownTimeout time.Duration

	// PollInterval is exposed for tests. Production callers leave it unset.
	PollInterval time.Duration
}

func (s *StepShutdown) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	if strings.TrimSpace(s.ShutdownCommand) == "" {
		return multistep.ActionContinue
	}

	ui := state.Get("ui").(packer.Ui)
	rawCommunicator, ok := state.GetOk("communicator")
	if !ok {
		return s.halt(state, ui, fmt.Errorf("shutdown_command requires an active communicator"))
	}
	comm, ok := rawCommunicator.(packer.Communicator)
	if !ok || comm == nil {
		return s.halt(state, ui, fmt.Errorf("invalid communicator in build state for shutdown_command"))
	}

	ui.Say("Executing the guest shutdown command...")
	cmd := &packer.RemoteCmd{Command: s.ShutdownCommand}
	commandErr := cmd.RunWithUi(ctx, comm, ui)
	if commandErr == nil {
		switch exitStatus := cmd.ExitStatus(); exitStatus {
		case 0, packer.CmdDisconnect:
			// A disconnect is expected when the guest powers off before the
			// communicator receives a final command status.
		default:
			return s.halt(state, ui, fmt.Errorf("shutdown command exited with status %d", exitStatus))
		}
	}

	timeout := s.ShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	ui.Sayf("Waiting up to %s for VirtualMachine %s/%s to shut down...", timeout, s.Namespace, s.Name)
	err := WaitForGuestPowerOff(ctx, s.Client, s.Namespace, s.Name, timeout, s.PollInterval, ui.Sayf)
	if err != nil {
		if commandErr != nil {
			err = fmt.Errorf(
				"wait for guest shutdown of VirtualMachine %s/%s: %w (shutdown command returned: %v)",
				s.Namespace, s.Name, err, commandErr,
			)
		} else {
			err = fmt.Errorf(
				"wait for guest shutdown of VirtualMachine %s/%s: %w",
				s.Namespace, s.Name, err,
			)
		}
		return s.halt(state, ui, err)
	}

	if commandErr != nil {
		// WinRM and SSH may report a transport error when the guest shuts down
		// before returning a final command status. The observed clean power-off
		// is authoritative.
		log.Printf("shutdown command disconnected after guest power-off: %v", commandErr)
	}
	ui.Say("Guest shut down.")
	return multistep.ActionContinue
}

// WaitForGuestPowerOff blocks until the guest powers itself off (VMI Succeeded,
// or the VMI is gone and the VM is Stopped), failing if the VMI enters Failed
// (which RerunOnFailure would restart, racing a live-disk capture). It is shared
// by the shutdown_command path and the no-communicator wait_for_shutdown install
// path. progress, when set, receives a throttled heartbeat during long waits.
func WaitForGuestPowerOff(ctx context.Context, client ShutdownClient, namespace, name string, timeout, pollInterval time.Duration, progress func(format string, args ...any)) error {
	if pollInterval <= 0 {
		pollInterval = defaultShutdownPollInterval
	}
	var lastReportedAt time.Time
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		done, err := guestPoweredOff(ctx, client, namespace, name)
		if err != nil || done {
			return done, err
		}
		if progress != nil && time.Since(lastReportedAt) >= time.Minute {
			progress("Still waiting for VirtualMachine %s/%s to power off...", namespace, name)
			lastReportedAt = time.Now()
		}
		return false, nil
	})
}

func guestPoweredOff(ctx context.Context, client ShutdownClient, namespace, name string) (bool, error) {
	vm, err := client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get VirtualMachine %s/%s while waiting for shutdown: %w", namespace, name, err)
	}

	vmi, err := client.VirtualMachineInstance(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Do not accept VMI absence alone: RerunOnFailure may briefly have no
		// VMI while replacing a failed one. The VM's stopped status confirms a
		// clean shutdown will remain stopped.
		return vm.Status.PrintableStatus == v1.VirtualMachineStatusStopped, nil
	}
	if err != nil {
		return false, fmt.Errorf("get VirtualMachineInstance %s/%s while waiting for shutdown: %w", namespace, name, err)
	}

	switch vmi.Status.Phase {
	case v1.Succeeded:
		return true, nil
	case v1.Failed:
		// RerunOnFailure intentionally restarts failed VMIs. Treating Failed as
		// powered off would race that restart and could capture a live disk.
		return false, fmt.Errorf(
			"VirtualMachineInstance %s/%s entered Failed phase instead of shutting down cleanly",
			namespace, name,
		)
	default:
		return false, nil
	}
}

func (s *StepShutdown) halt(state multistep.StateBag, ui packer.Ui, err error) multistep.StepAction {
	state.Put("error", err)
	ui.Error(err.Error())
	return multistep.ActionHalt
}

func (s *StepShutdown) Cleanup(multistep.StateBag) {}

// RunStrategyForSelfPowerOff keeps the historical Always behavior unless the
// build expects the guest to power ITSELF off -- either after a shutdown_command
// or during a no-communicator install with wait_for_shutdown. In those cases
// RerunOnFailure still starts the VM and restarts crashes, but a clean guest
// power-off remains stopped for capture (Always would auto-restart it, e.g.
// re-running a just-generalized Windows image through OOBE, or rebooting an
// appliance back into its installer).
func RunStrategyForSelfPowerOff(shutdownCommand string, waitForShutdown bool) v1.VirtualMachineRunStrategy {
	if strings.TrimSpace(shutdownCommand) != "" || waitForShutdown {
		return v1.RunStrategyRerunOnFailure
	}
	return v1.RunStrategyAlways
}
