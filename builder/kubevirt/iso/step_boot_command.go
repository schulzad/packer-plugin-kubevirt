// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/bootcommand"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"github.com/mitchellh/go-vnc"

	"kubevirt.io/client-go/kubecli"
)

type StepBootCommand struct {
	config Config
	client kubecli.KubevirtClient
}

func (s *StepBootCommand) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.config.Name
	namespace := s.config.Namespace
	bootCommand := strings.Join(s.config.BootCommand, "")
	bootWait := s.config.BootWait

	if int64(bootWait) > 0 {
		ui.Sayf("Waiting %s to boot...", bootWait.String())

		select {
		case <-time.After(bootWait):
			break
		case <-ctx.Done():
			return multistep.ActionHalt
		}
	}

	streamInterface, err := s.client.VirtualMachineInstance(namespace).VNC(name)
	if err != nil {
		return s.halt(state, ui, fmt.Errorf("open VNC to VirtualMachineInstance %s/%s: %w", namespace, name, err))
	}

	connection, err := vnc.Client(streamInterface.AsConn(), &vnc.ClientConfig{})
	if err != nil {
		return s.halt(state, ui, fmt.Errorf("establish VNC client for %s/%s: %w", namespace, name, err))
	}

	ui.Say("Typing the boot command over VNC...")

	command, err := interpolate.Render(bootCommand, &interpolate.Context{})
	if err != nil {
		return s.halt(state, ui, fmt.Errorf("render boot_command: %w", err))
	}

	sequence, err := bootcommand.GenerateExpressionSequence(command)
	if err != nil {
		return s.halt(state, ui, fmt.Errorf("parse boot_command: %w", err))
	}

	driver := bootcommand.NewVNCDriver(connection, time.Duration(0))
	if err := sequence.Do(ctx, driver); err != nil {
		return s.halt(state, ui, fmt.Errorf("send boot_command over VNC to %s/%s: %w", namespace, name, err))
	}
	return multistep.ActionContinue
}

func (s *StepBootCommand) halt(state multistep.StateBag, ui packer.Ui, err error) multistep.StepAction {
	state.Put("error", err)
	ui.Error(err.Error())
	return multistep.ActionHalt
}

func (s *StepBootCommand) Cleanup(state multistep.StateBag) {
	// Left blank intentionally
}
