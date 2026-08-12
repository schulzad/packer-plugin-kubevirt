// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package image

import (
	"context"
	"fmt"

	ssh "golang.org/x/crypto/ssh"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/communicator"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/multistep/commonsteps"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/packerbuilderdata"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"kubevirt.io/client-go/kubecli"

	kubevirtcommon "github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/common"
	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso"
)

type Builder struct {
	config    Config
	runner    multistep.Runner
	client    kubecli.KubevirtClient
	clientset *kubernetes.Clientset
}

func (b *Builder) ConfigSpec() hcldec.ObjectSpec {
	return b.config.FlatMapstructure().HCL2Spec()
}

func (b *Builder) Prepare(raws ...interface{}) ([]string, []string, error) {
	warnings, errs := b.config.Prepare(raws...)
	if errs != nil {
		return nil, warnings, errs
	}

	kubeConfig := b.config.KubeConfig
	if kubeConfig == "" {
		return nil, warnings, fmt.Errorf("KUBECONFIG environment variable is not set")
	}

	client, err := kubecli.GetKubevirtClientFromFlags("", kubeConfig)
	if err != nil {
		return nil, warnings, fmt.Errorf("failed to get kubevirt client: %w", err)
	}
	b.client = client

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return nil, warnings, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, warnings, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}
	b.clientset = clientset
	return []string{"BootableVolumeName"}, warnings, nil
}

func (b *Builder) Run(ctx context.Context, ui packer.Ui, hook packer.Hook) (packer.Artifact, error) {
	state := new(multistep.BasicStateBag)
	state.Put("hook", hook)
	state.Put("ui", ui)

	generatedData := &packerbuilderdata.GeneratedData{State: state}

	// The validate/port-forward/stop/finalize steps are shared with the
	// kubevirt-iso builder and are typed to iso.Config, so adapt this builder's
	// config to the shared fields they read.
	shared := b.sharedISOConfig()

	steps := []multistep.Step{
		&iso.StepValidateBootableVolume{
			Config: shared,
			Client: b.client,
		},
		&StepCreateVM{
			Config: b.config,
			Client: b.client,
		},
	}

	if b.config.Communicator == "ssh" {
		steps = append(steps, b.buildSSHSteps(shared)...)
	}
	if b.config.Communicator == "winrm" {
		steps = append(steps, b.buildWinRMSteps(shared)...)
	}

	steps = append(steps,
		&kubevirtcommon.StepShutdown{
			Client:          b.client,
			Name:            b.config.Name,
			Namespace:       b.config.Namespace,
			ShutdownCommand: b.config.ShutdownCommand,
			ShutdownTimeout: b.config.ShutdownTimeout,
		},
		&iso.StepStopVirtualMachine{
			Config: shared,
			Client: b.client,
		},
	)

	if !b.config.SkipCreateImage {
		steps = append(steps, &iso.StepCreateBootableVolume{
			Config:        shared,
			Client:        b.client,
			GeneratedData: generatedData,
		})
	}

	b.runner = commonsteps.NewRunner(steps, b.config.PackerConfig, ui)
	b.runner.Run(ctx, state)

	if rawErr, ok := state.GetOk("error"); ok {
		return nil, rawErr.(error)
	}
	if _, ok := state.GetOk(multistep.StateCancelled); ok {
		return nil, nil
	}

	if b.config.SkipCreateImage {
		return nil, nil
	}

	bootableVolumeName, ok := state.Get("bootable_volume_name").(string)
	if !ok || bootableVolumeName == "" {
		return nil, fmt.Errorf("bootable volume name not found in state")
	}
	namespace, _ := state.Get("bootable_volume_namespace").(string)

	return &iso.Artifact{
		Name:      bootableVolumeName,
		Namespace: namespace,
		BuilderID: "packer.kubevirt.image",
		StateData: map[string]any{
			"generated_data": state.Get("generated_data"),
		},
	}, nil
}

// sharedISOConfig projects this builder's config onto the fields the reused
// kubevirt-iso steps read (validation, port-forward, stop, finalize).
func (b *Builder) sharedISOConfig() iso.Config {
	c := b.config
	return iso.Config{
		PackerConfig:        c.PackerConfig,
		KubeConfig:          c.KubeConfig,
		Name:                c.Name,
		Namespace:           c.Namespace,
		DiskSize:            c.DiskSize,
		InstanceType:        c.InstanceType,
		InstanceTypeKind:    c.InstanceTypeKind,
		Preference:          c.Preference,
		PreferenceKind:      c.PreferenceKind,
		OperatingSystemType: c.OperatingSystemType,
		DiskInterface:       c.DiskInterface,
		Communicator:        c.Communicator,
		SSHHost:             c.SSHHost,
		SSHLocalPort:        c.SSHLocalPort,
		SSHRemotePort:       c.SSHRemotePort,
		SSHUsername:         c.SSHUsername,
		SSHPassword:         c.SSHPassword,
		SSHWaitTimeout:      c.SSHWaitTimeout,
		WinRMHost:           c.WinRMHost,
		WinRMLocalPort:      c.WinRMLocalPort,
		WinRMRemotePort:     c.WinRMRemotePort,
		WinRMUsername:       c.WinRMUsername,
		WinRMPassword:       c.WinRMPassword,
		WinRMWaitTimeout:    c.WinRMWaitTimeout,
		KeepVM:              c.KeepVM,
		SkipCreateImage:     c.SkipCreateImage,
	}
}

func (b *Builder) buildSSHSteps(shared iso.Config) []multistep.Step {
	commConfig := &communicator.Config{
		Type: b.config.Communicator,
		SSH: communicator.SSH{
			SSHHost:     b.config.SSHHost,
			SSHPort:     b.config.SSHLocalPort,
			SSHUsername: b.config.SSHUsername,
			SSHPassword: b.config.SSHPassword,
			SSHTimeout:  b.config.SSHWaitTimeout,
		},
	}
	_ = commConfig.Prepare(&interpolate.Context{})

	return []multistep.Step{
		&iso.StepStartPortForward{
			Config:        shared,
			Client:        b.client,
			ForwarderFunc: iso.DefaultPortForwarder,
		},
		&communicator.StepConnect{
			Config: commConfig,
			Host: func(multistep.StateBag) (string, error) {
				return b.config.SSHHost, nil
			},
			SSHConfig: func(multistep.StateBag) (*ssh.ClientConfig, error) {
				return &ssh.ClientConfig{
					User:            b.config.SSHUsername,
					Auth:            []ssh.AuthMethod{ssh.Password(b.config.SSHPassword)},
					HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				}, nil
			},
			SSHPort: func(multistep.StateBag) (int, error) {
				return b.config.SSHLocalPort, nil
			},
		},
		&commonsteps.StepProvision{},
	}
}

func (b *Builder) buildWinRMSteps(shared iso.Config) []multistep.Step {
	commConfig := &communicator.Config{
		Type: b.config.Communicator,
		WinRM: communicator.WinRM{
			WinRMHost:     b.config.WinRMHost,
			WinRMPort:     b.config.WinRMLocalPort,
			WinRMUser:     b.config.WinRMUsername,
			WinRMPassword: b.config.WinRMPassword,
			WinRMTimeout:  b.config.WinRMWaitTimeout,
		},
	}
	_ = commConfig.Prepare(&interpolate.Context{})

	return []multistep.Step{
		&iso.StepStartPortForward{
			Config:        shared,
			Client:        b.client,
			ForwarderFunc: iso.DefaultPortForwarder,
		},
		&communicator.StepConnect{
			Config: commConfig,
			Host: func(multistep.StateBag) (string, error) {
				return b.config.WinRMHost, nil
			},
			WinRMConfig: func(multistep.StateBag) (*communicator.WinRMConfig, error) {
				return &communicator.WinRMConfig{
					Username: b.config.WinRMUsername,
					Password: b.config.WinRMPassword,
				}, nil
			},
			WinRMPort: func(multistep.StateBag) (int, error) {
				return b.config.WinRMLocalPort, nil
			},
		},
		&commonsteps.StepProvision{},
	}
}
