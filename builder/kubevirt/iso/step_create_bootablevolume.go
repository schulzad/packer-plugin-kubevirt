// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/packerbuilderdata"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"kubevirt.io/client-go/kubecli"
)

type StepCreateBootableVolume struct {
	Config        Config
	Client        kubecli.KubevirtClient
	GeneratedData *packerbuilderdata.GeneratedData
}

func (s *StepCreateBootableVolume) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace
	diskSize := s.Config.DiskSize
	instanceType := s.Config.InstanceType
	preferenceName := s.Config.Preference
	cloneVolume := cloneVolume(name, namespace, diskSize)
	sourceVolume := sourceVolume(name, namespace, instanceType, preferenceName)

	// With `packer build -force`, replace any bootable volume objects left over
	// from a previous build so the clone/create below don't fail with
	// AlreadyExists. Without -force, StepValidateBootableVolume has already
	// failed fast if these exist.
	if s.Config.PackerForce {
		if err := deleteBootableVolumeArtifacts(ctx, s.Client, ui, namespace, name); err != nil {
			state.Put("error", err)
			ui.Error(err.Error())
			return multistep.ActionHalt
		}
	}

	ui.Sayf("Creating a new bootable volume (%s/%s)...", namespace, name)

	dv, err := s.Client.CdiClient().CdiV1beta1().DataVolumes(namespace).Create(ctx, cloneVolume, metav1.CreateOptions{})
	if err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	if err = WaitUntilDataVolumeSucceeded(ctx, s.Client, dv.Namespace, dv.Name); err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	ds, err := s.Client.CdiClient().CdiV1beta1().DataSources(namespace).Create(ctx, sourceVolume, metav1.CreateOptions{})
	if err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	state.Put("bootable_volume_name", ds.Name)
	state.Put("bootable_volume_namespace", namespace)
	s.GeneratedData.Put("BootableVolumeName", ds.Name)
	return multistep.ActionContinue
}

func (s *StepCreateBootableVolume) Cleanup(state multistep.StateBag) {
	// Left blank intentionally
}

// bootableVolumeArtifactsExist reports which of the golden DataSource,
// DataVolume, and PVC (all named `name`) already exist in the namespace. It is
// used both by the preflight (fast-fail) and to decide what to clean under
// -force.
func bootableVolumeArtifactsExist(ctx context.Context, client kubecli.KubevirtClient, namespace, name string) ([]string, error) {
	var existing []string

	if _, err := client.CdiClient().CdiV1beta1().DataSources(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		existing = append(existing, "datasource/"+name)
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}

	if _, err := client.CdiClient().CdiV1beta1().DataVolumes(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		existing = append(existing, "datavolume/"+name)
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}

	if _, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		existing = append(existing, "persistentvolumeclaim/"+name)
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}

	return existing, nil
}

// deleteBootableVolumeArtifacts removes a prior build's golden objects so a new
// clone can reuse the name. Order matters: the DataSource references the PVC, so
// delete it first; then the DataVolume; then the PVC directly (CDI's DataVolume
// garbage collection may have already removed the DataVolume, leaving the PVC
// standalone). Finally wait until the PVC is gone, since a clone into an
// existing PVC name fails.
func deleteBootableVolumeArtifacts(ctx context.Context, client kubecli.KubevirtClient, ui packer.Ui, namespace, name string) error {
	ui.Sayf("Removing existing bootable volume objects (%s/%s) for -force...", namespace, name)

	if err := client.CdiClient().CdiV1beta1().DataSources(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := client.CdiClient().CdiV1beta1().DataVolumes(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := client.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	pollInterval := 5 * time.Second
	pollTimeout := 600 * time.Second
	return wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
}
