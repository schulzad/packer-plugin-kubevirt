// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	fakecdiclient "kubevirt.io/client-go/containerizeddataimporter/fake"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

func TestResolveExistingWaitsForSucceeded(t *testing.T) {
	ctx := context.Background()
	cdiClient := fakecdiclient.NewSimpleClientset(&cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "user-iso", Namespace: "images"},
		Status:     cdiv1.DataVolumeStatus{Phase: cdiv1.Succeeded},
	})
	manager := &Manager{CDI: cdiClient}

	result, err := manager.Stage(ctx, Options{
		Namespace:      "images",
		ExistingVolume: "user-iso",
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("resolve existing DataVolume: %v", err)
	}
	if result.Owned {
		t.Fatal("an existing DataVolume must never be owned by the plugin")
	}
	if result.Kind != SourceExisting || result.VolumeName != "user-iso" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestWaitForDataVolumeReturnsFailedCondition(t *testing.T) {
	client := fakecdiclient.NewSimpleClientset(&cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "images"},
		Status: cdiv1.DataVolumeStatus{
			Phase: cdiv1.Failed,
			Conditions: []cdiv1.DataVolumeCondition{{
				Message: "checksum did not match",
			}},
		},
	})

	_, err := WaitForDataVolume(context.Background(), client, "images", "failed", time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum did not match") {
		t.Fatalf("expected failed condition, got %v", err)
	}
}

func TestStageHTTPDataVolume(t *testing.T) {
	ctx := context.Background()
	cdiClient := fakecdiclient.NewSimpleClientset()
	makeDataVolumeSucceed(cdiClient)
	manager := &Manager{CDI: cdiClient}

	result, err := manager.Stage(ctx, Options{
		Namespace:    "images",
		HTTPURL:      "https://mirror.example.test/rocky.iso",
		VolumeName:   "rocky-iso",
		StorageSize:  "2Gi",
		Checksum:     "sha256:" + strings.Repeat("a", 64),
		StorageClass: "longhorn",
		Timeout:      time.Second,
	})
	if err != nil {
		t.Fatalf("stage HTTP ISO: %v", err)
	}
	if !result.Owned || result.Kind != SourceHTTP || result.VolumeName != "rocky-iso" || result.VolumeUID == "" {
		t.Fatalf("unexpected result: %#v", result)
	}

	dv, err := cdiClient.CdiV1beta1().DataVolumes("images").Get(ctx, "rocky-iso", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get staged DataVolume: %v", err)
	}
	if dv.Spec.Source == nil || dv.Spec.Source.HTTP == nil {
		t.Fatal("expected HTTP DataVolume source")
	}
	if dv.Spec.Source.HTTP.Checksum != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("checksum = %q", dv.Spec.Source.HTTP.Checksum)
	}
	if dv.Annotations[AnnotationImportState] != StateReady {
		t.Fatalf("import state = %q, want ready", dv.Annotations[AnnotationImportState])
	}
}

func TestStageHTTPRejectsPrunedChecksum(t *testing.T) {
	ctx := context.Background()
	cdiClient := fakecdiclient.NewSimpleClientset()
	makeDataVolumeSucceed(cdiClient)
	// Simulate an API server (CDI < 1.65) that drops the unknown checksum field.
	cdiClient.Fake.PrependReactor("create", "datavolumes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		dv := action.(k8stesting.CreateAction).GetObject().(*cdiv1.DataVolume)
		dv.Spec.Source.HTTP.Checksum = ""
		return false, nil, nil
	})
	manager := &Manager{CDI: cdiClient}

	result, err := manager.Stage(ctx, Options{
		Namespace:   "images",
		HTTPURL:     "https://mirror.example.test/rocky.iso",
		VolumeName:  "rocky-iso",
		StorageSize: "2Gi",
		Checksum:    "sha256:" + strings.Repeat("a", 64),
		Timeout:     time.Second,
		Retain:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "CDI 1.65+") {
		t.Fatalf("expected unsupported checksum error, got %v", err)
	}
	if !result.Owned {
		t.Fatal("created DataVolume must remain owned so cleanup can remove it")
	}
	if result.Retain {
		t.Fatal("a pruned DataVolume must not be retained as a cache entry")
	}
}

func TestStageHTTPReusesMatchingManagedVolume(t *testing.T) {
	ctx := context.Background()
	identity := Identity{Kind: SourceHTTP}
	existing := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "rocky-iso",
			Namespace:   "images",
			UID:         k8stypes.UID("keep-uid"),
			Labels:      managedLabels(),
			Annotations: managedAnnotations(withURLHash(identity, "https://mirror.example.test/rocky.iso"), StateReady),
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{HTTP: &cdiv1.DataVolumeSourceHTTP{URL: "https://mirror.example.test/rocky.iso"}},
			Storage: &cdiv1.StorageSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
			}},
		},
		Status: cdiv1.DataVolumeStatus{Phase: cdiv1.Succeeded},
	}
	cdiClient := fakecdiclient.NewSimpleClientset(existing)
	manager := &Manager{CDI: cdiClient}

	result, err := manager.Stage(ctx, Options{
		Namespace:   "images",
		HTTPURL:     "https://mirror.example.test/rocky.iso",
		VolumeName:  "rocky-iso",
		StorageSize: "2Gi",
		Retain:      true,
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("reuse managed DataVolume: %v", err)
	}
	if result.VolumeUID != "keep-uid" {
		t.Fatalf("expected reuse of existing volume, got UID %q", result.VolumeUID)
	}
}

func TestCleanupDeletesOnlyManagedDataVolume(t *testing.T) {
	ctx := context.Background()
	dv := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "managed-iso",
			Namespace:   "images",
			UID:         k8stypes.UID("managed-uid"),
			Labels:      managedLabels(),
			Annotations: managedAnnotations(Identity{Kind: SourceHTTP}, StateReady),
		},
	}
	cdiClient := fakecdiclient.NewSimpleClientset(dv)
	manager := &Manager{CDI: cdiClient}

	err := manager.Cleanup(ctx, "images", Result{
		VolumeName: "managed-iso",
		VolumeUID:  "managed-uid",
		Owned:      true,
		Kind:       SourceHTTP,
		Identity:   Identity{Kind: SourceHTTP},
	})
	if err != nil {
		t.Fatalf("cleanup managed DataVolume: %v", err)
	}
	if _, err := cdiClient.CdiV1beta1().DataVolumes("images").Get(ctx, "managed-iso", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("managed DataVolume still exists: %v", err)
	}
}

func TestCleanupRefusesReplacementDataVolumeUID(t *testing.T) {
	ctx := context.Background()
	dv := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "managed-iso",
			Namespace:   "images",
			UID:         k8stypes.UID("replacement-uid"),
			Labels:      managedLabels(),
			Annotations: managedAnnotations(Identity{Kind: SourceHTTP}, StateReady),
		},
	}
	cdiClient := fakecdiclient.NewSimpleClientset(dv)
	manager := &Manager{CDI: cdiClient}

	err := manager.Cleanup(ctx, "images", Result{
		VolumeName: "managed-iso",
		VolumeUID:  "original-uid",
		Owned:      true,
		Kind:       SourceHTTP,
		Identity:   Identity{Kind: SourceHTTP},
	})
	if err == nil || !strings.Contains(err.Error(), "replacement") {
		t.Fatalf("expected replacement UID refusal, got %v", err)
	}
	if _, err := cdiClient.CdiV1beta1().DataVolumes("images").Get(ctx, "managed-iso", metav1.GetOptions{}); err != nil {
		t.Fatalf("replacement DataVolume was deleted: %v", err)
	}
}

func TestCleanupPreservesRetainedAndExternalVolumes(t *testing.T) {
	ctx := context.Background()
	cdiClient := fakecdiclient.NewSimpleClientset()
	manager := &Manager{CDI: cdiClient}

	// Neither an external nor a retained result should trigger any deletion.
	for _, result := range []Result{
		{VolumeName: "user-iso", Owned: false, Kind: SourceExisting},
		{VolumeName: "cache-iso", Owned: true, Retain: true, Kind: SourceHTTP},
	} {
		if err := manager.Cleanup(ctx, "images", result); err != nil {
			t.Fatalf("cleanup must be a no-op for %#v: %v", result, err)
		}
	}
}

// makeDataVolumeSucceed marks created DataVolumes Succeeded with a UID so tests
// exercise the ready/import path without a running CDI controller.
func makeDataVolumeSucceed(cdiClient *fakecdiclient.Clientset) {
	cdiClient.Fake.PrependReactor("create", "datavolumes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		dv := action.(k8stesting.CreateAction).GetObject().(*cdiv1.DataVolume)
		if dv.UID == "" {
			dv.UID = k8stypes.UID("uid-" + dv.Name)
		}
		dv.Status.Phase = cdiv1.Succeeded
		return false, nil, nil
	})
}

func withURLHash(identity Identity, url string) Identity {
	digest := sha256.Sum256([]byte(url))
	identity.URLHash = hex.EncodeToString(digest[:])
	return identity
}
