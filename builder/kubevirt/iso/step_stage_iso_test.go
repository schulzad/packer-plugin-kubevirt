// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

package iso_test

import (
	"context"
	"io"
	"strings"

	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso"
	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso/staging"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	fakecdiclient "kubevirt.io/client-go/containerizeddataimporter/fake"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

var _ = Describe("StepStageISO", func() {
	const (
		namespace  = "images"
		volumeName = "rocky-install-iso"
	)

	var (
		state     *multistep.BasicStateBag
		cdiClient *fakecdiclient.Clientset
		step      *iso.StepStageISO
	)

	BeforeEach(func() {
		state = new(multistep.BasicStateBag)
		state.Put("ui", &packer.BasicUi{
			Reader:      strings.NewReader(""),
			Writer:      io.Discard,
			ErrorWriter: io.Discard,
		})
		cdiClient = fakecdiclient.NewSimpleClientset()
		step = &iso.StepStageISO{
			Config: iso.Config{
				Namespace:     namespace,
				IsoVolumeName: volumeName,
			},
			Manager: &staging.Manager{CDI: cdiClient},
		}
	})

	It("publishes a validated existing DataVolume into build state", func() {
		_, err := cdiClient.CdiV1beta1().DataVolumes(namespace).Create(
			context.Background(),
			&cdiv1.DataVolume{
				ObjectMeta: metav1.ObjectMeta{Name: volumeName, Namespace: namespace},
				Status:     cdiv1.DataVolumeStatus{Phase: cdiv1.Succeeded},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())

		action := step.Run(context.Background(), state)

		Expect(action).To(Equal(multistep.ActionContinue))
		Expect(state.Get("iso_volume_name")).To(Equal(volumeName))

		// An existing (external) DataVolume must survive cleanup.
		step.Cleanup(state)
		_, err = cdiClient.CdiV1beta1().DataVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("preserves the original staging error in build state", func() {
		action := step.Run(context.Background(), state)

		Expect(action).To(Equal(multistep.ActionHalt))
		rawErr, ok := state.GetOk("error")
		Expect(ok).To(BeTrue())
		Expect(rawErr).To(MatchError(ContainSubstring("not found")))
	})

	It("imports an HTTP URL and cleans up the managed DataVolume", func() {
		cdiClient.Fake.PrependReactor("create", "datavolumes", func(action k8stesting.Action) (bool, runtime.Object, error) {
			dv := action.(k8stesting.CreateAction).GetObject().(*cdiv1.DataVolume)
			dv.Status.Phase = cdiv1.Succeeded
			return false, nil, nil
		})
		step.Config.IsoVolumeName = ""
		step.Config.IsoURL = "https://mirror.example.test/rocky.iso"
		step.Config.IsoStagingName = volumeName
		step.Config.IsoStorageSize = "2Gi"

		action := step.Run(context.Background(), state)
		Expect(action).To(Equal(multistep.ActionContinue))
		Expect(state.Get("iso_volume_name")).To(Equal(volumeName))

		step.Cleanup(state)
		_, err := cdiClient.CdiV1beta1().DataVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("retains managed media when the build is halted so a re-run reuses the import", func() {
		_, err := cdiClient.CdiV1beta1().DataVolumes(namespace).Create(
			context.Background(),
			&cdiv1.DataVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        volumeName,
					Namespace:   namespace,
					Labels:      map[string]string{staging.LabelManagedBy: staging.ManagedByValue},
					Annotations: map[string]string{staging.AnnotationManaged: "true"},
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())
		state.Put("iso_staging_result", staging.Result{
			VolumeName: volumeName,
			Owned:      true,
			Kind:       staging.SourceHTTP,
		})
		state.Put(multistep.StateHalted, true)

		step.Cleanup(state)

		_, err = cdiClient.CdiV1beta1().DataVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("retains managed media when the temporary VM did not detach", func() {
		_, err := cdiClient.CdiV1beta1().DataVolumes(namespace).Create(
			context.Background(),
			&cdiv1.DataVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        volumeName,
					Namespace:   namespace,
					Labels:      map[string]string{staging.LabelManagedBy: staging.ManagedByValue},
					Annotations: map[string]string{staging.AnnotationManaged: "true"},
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())
		state.Put("iso_staging_result", staging.Result{
			VolumeName: volumeName,
			Owned:      true,
			Kind:       staging.SourceHTTP,
		})
		state.Put("temporary_vm_created", true)

		step.Cleanup(state)

		_, err = cdiClient.CdiV1beta1().DataVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})
})
