// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

// Package staging resolves the kubevirt-iso builder's installation media into a
// single CDI DataVolume the temporary VM can attach as its CD-ROM. It supports
// two sources: an existing, user-managed DataVolume referenced by name, and an
// HTTP(S) URL that CDI imports inside the cluster. CDI performs all data
// movement; this package only creates, validates, and waits on DataVolume
// objects. Managed media is kept after the build for reuse; it is removed only
// when a forced re-import (`packer build -force`) replaces it.
package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const (
	// AnnotationManaged marks a DataVolume created by this plugin so cleanup
	// never touches user-owned media.
	AnnotationManaged = "packer.hashicorp.com/iso-managed"
	// AnnotationSourceKind records which source produced the DataVolume.
	AnnotationSourceKind = "packer.hashicorp.com/iso-source-kind"
	// AnnotationSourceURLHash records the SHA-256 of the requested HTTP URL so a
	// retained cache entry is only reused for the same source.
	AnnotationSourceURLHash = "packer.hashicorp.com/iso-source-url-sha256"
	// AnnotationImportState records the plugin's view of the import lifecycle.
	AnnotationImportState = "packer.hashicorp.com/iso-import-state"

	// LabelManagedBy and LabelStaging identify plugin-created staging objects.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelStaging   = "packer.hashicorp.com/iso-staging"

	ManagedByValue = "packer-plugin-kubevirt"

	StateImporting = "importing"
	StateReady     = "ready"
	StateFailed    = "failed"

	immediateBindingAnnotation = "cdi.kubevirt.io/storage.bind.immediate.requested"
)

// SourceKind identifies which installation-media source resolved a DataVolume.
type SourceKind string

const (
	SourceExisting SourceKind = "existing"
	SourceHTTP     SourceKind = "http"
)

// Identity captures the immutable source fingerprint recorded on a managed
// DataVolume so a retained object is only reused for the same media.
type Identity struct {
	Kind     SourceKind
	URLHash  string
	Checksum string
}

// Options configures a single staging resolution. Exactly one of ExistingVolume
// or HTTPURL is set (the builder config enforces this).
type Options struct {
	Namespace         string
	ExistingVolume    string
	HTTPURL           string
	VolumeName        string
	StorageSize       string
	StorageClass      string
	Checksum          string
	HTTPSecretRef     string
	HTTPCertConfigMap string
	Timeout           time.Duration
	Force             bool
	Progress          func(format string, args ...any)
}

// Result is the resolved DataVolume plus the ownership metadata later steps need.
type Result struct {
	VolumeName string
	VolumeUID  string
	Owned      bool
	Kind       SourceKind
}

// Manager resolves ISO sources against a CDI client.
type Manager struct {
	CDI cdiclient.Interface
}

// Stage resolves the configured source into a ready DataVolume.
func (m *Manager) Stage(ctx context.Context, opts Options) (Result, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	switch {
	case opts.ExistingVolume != "":
		return m.resolveExisting(ctx, opts)
	case opts.HTTPURL != "":
		return m.stageHTTP(ctx, opts)
	default:
		return Result{}, fmt.Errorf("no ISO source configured")
	}
}

func (m *Manager) resolveExisting(ctx context.Context, opts Options) (Result, error) {
	result := Result{
		VolumeName: opts.ExistingVolume,
		Kind:       SourceExisting,
	}
	if opts.Progress != nil {
		opts.Progress("Validating existing ISO DataVolume (%s/%s)...", opts.Namespace, opts.ExistingVolume)
	}
	if _, err := m.CDI.CdiV1beta1().DataVolumes(opts.Namespace).Get(ctx, opts.ExistingVolume, metav1.GetOptions{}); err != nil {
		return result, fmt.Errorf("get ISO DataVolume %s/%s: %w", opts.Namespace, opts.ExistingVolume, err)
	}
	if _, err := WaitForDataVolume(ctx, m.CDI, opts.Namespace, opts.ExistingVolume, opts.Timeout, opts.Progress); err != nil {
		return result, err
	}
	return result, nil
}

func (m *Manager) stageHTTP(ctx context.Context, opts Options) (Result, error) {
	urlDigest := sha256.Sum256([]byte(opts.HTTPURL))
	identity := Identity{
		Kind:     SourceHTTP,
		URLHash:  hex.EncodeToString(urlDigest[:]),
		Checksum: strings.ToLower(opts.Checksum),
	}
	result := Result{
		VolumeName: opts.VolumeName,
		Kind:       SourceHTTP,
	}

	desired, err := httpDataVolume(opts, identity)
	if err != nil {
		return result, err
	}
	if opts.Progress != nil {
		opts.Progress("Importing ISO URL into DataVolume (%s/%s)...", opts.Namespace, opts.VolumeName)
	}

	dv, err := m.CDI.CdiV1beta1().DataVolumes(opts.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		dv, err = m.CDI.CdiV1beta1().DataVolumes(opts.Namespace).Get(ctx, opts.VolumeName, metav1.GetOptions{})
		if err == nil {
			err = validateHTTPDataVolume(dv, opts, identity)
		}
		if err != nil && opts.Force && isManaged(dv) {
			if deleteErr := m.deleteManagedDataVolume(ctx, opts.Namespace, opts.VolumeName, string(dv.UID)); deleteErr != nil {
				return result, deleteErr
			}
			dv, err = m.CDI.CdiV1beta1().DataVolumes(opts.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		}
	}
	if err != nil {
		if opts.Checksum != "" {
			return result, fmt.Errorf(
				"create or reuse HTTP ISO DataVolume %s/%s (iso_checksum requires CDI 1.65+): %w",
				opts.Namespace, opts.VolumeName, err)
		}
		return result, fmt.Errorf("create or reuse HTTP ISO DataVolume %s/%s: %w", opts.Namespace, opts.VolumeName, err)
	}
	result.Owned = true
	result.VolumeUID = string(dv.UID)

	// Replace a previously failed managed import when -force is set.
	if dv.Status.Phase == cdiv1.Failed && opts.Force {
		if err := m.deleteManagedDataVolume(ctx, opts.Namespace, opts.VolumeName, string(dv.UID)); err != nil {
			return result, err
		}
		dv, err = m.CDI.CdiV1beta1().DataVolumes(opts.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			return result, fmt.Errorf("recreate failed HTTP ISO DataVolume %s/%s: %w", opts.Namespace, opts.VolumeName, err)
		}
		result.VolumeUID = string(dv.UID)
	}

	// An API server that silently prunes the requested source (e.g. CDI < 1.65
	// dropping spec.source.http.checksum) must never be accepted as valid media,
	// and such a pruned object is never reused: validateHTTPDataVolume rejects it
	// again on a later run, so a fresh import requires `packer build -force`.
	if err := validateHTTPDataVolume(dv, opts, identity); err != nil {
		return result, fmt.Errorf(
			"the API server did not preserve the requested HTTP ISO source (iso_checksum requires CDI 1.65+): %w", err)
	}

	if _, err := WaitForDataVolume(ctx, m.CDI, opts.Namespace, opts.VolumeName, opts.Timeout, opts.Progress); err != nil {
		markCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = m.patchImportState(markCtx, opts.Namespace, opts.VolumeName, StateFailed)
		cancel()
		return result, err
	}
	if err := m.patchImportState(ctx, opts.Namespace, opts.VolumeName, StateReady); err != nil {
		return result, fmt.Errorf("mark HTTP ISO DataVolume ready: %w", err)
	}
	return result, nil
}

// deleteManagedDataVolume removes a plugin-created DataVolume, refusing to touch
// anything without the plugin's ownership markers or whose UID no longer matches
// the object this build created. It backs forced re-imports (`packer build
// -force`); a normal build always retains its managed installation media.
func (m *Manager) deleteManagedDataVolume(ctx context.Context, namespace, name, expectedUID string) error {
	dv, err := m.CDI.CdiV1beta1().DataVolumes(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get managed ISO DataVolume %s/%s before deletion: %w", namespace, name, err)
	}
	if !isManaged(dv) {
		return fmt.Errorf("refusing to delete ISO DataVolume %s/%s without plugin ownership annotation", namespace, name)
	}
	if expectedUID != "" && string(dv.UID) != expectedUID {
		return fmt.Errorf(
			"refusing to delete replacement ISO DataVolume %s/%s: UID changed from %s to %s",
			namespace, name, expectedUID, dv.UID)
	}

	deleteOptions := metav1.DeleteOptions{}
	if dv.UID != "" {
		uid := dv.UID
		deleteOptions.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	if err := m.CDI.CdiV1beta1().DataVolumes(namespace).Delete(ctx, name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete managed ISO DataVolume %s/%s: %w", namespace, name, err)
	}
	// CDI garbage-collects the owner-referenced PVC with the DataVolume, so
	// waiting for the DataVolume to disappear frees the name for reuse.
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := m.CDI.CdiV1beta1().DataVolumes(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// WaitForDataVolume polls until the DataVolume reports Succeeded, failing fast on
// a Failed phase and surfacing CDI's condition messages.
func WaitForDataVolume(
	ctx context.Context,
	client cdiclient.Interface,
	namespace, name string,
	timeout time.Duration,
	progress func(format string, args ...any),
) (*cdiv1.DataVolume, error) {
	if timeout <= 0 {
		timeout = time.Hour
	}
	var latest *cdiv1.DataVolume
	var lastReport string
	var lastReportedAt time.Time
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		dv, err := client.CdiV1beta1().DataVolumes(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		latest = dv
		report := fmt.Sprintf("phase=%s", dv.Status.Phase)
		if dv.Status.Progress != "" {
			report += fmt.Sprintf(" progress=%s", dv.Status.Progress)
		}
		if progress != nil && (report != lastReport || time.Since(lastReportedAt) >= time.Minute) {
			progress("ISO DataVolume %s: %s", name, report)
			lastReport = report
			lastReportedAt = time.Now()
		}
		switch dv.Status.Phase {
		case cdiv1.Succeeded:
			return true, nil
		case cdiv1.Failed:
			return false, fmt.Errorf("ISO DataVolume %s/%s failed: %s", namespace, name, dataVolumeConditionMessage(dv))
		default:
			return false, nil
		}
	})
	if err != nil {
		return latest, fmt.Errorf("wait for ISO DataVolume %s/%s: %w", namespace, name, err)
	}
	return latest, nil
}

func httpDataVolume(opts Options, identity Identity) (*cdiv1.DataVolume, error) {
	size, err := resource.ParseQuantity(opts.StorageSize)
	if err != nil {
		return nil, fmt.Errorf("parse ISO storage size %q: %w", opts.StorageSize, err)
	}
	storage := &cdiv1.StorageSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: size},
		},
	}
	if opts.StorageClass != "" {
		storage.StorageClassName = ptr.To(opts.StorageClass)
	}
	annotations := managedAnnotations(identity, StateImporting)
	annotations[immediateBindingAnnotation] = "true"
	return &cdiv1.DataVolume{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cdiv1.CDIGroupVersionKind.GroupVersion().String(),
			Kind:       "DataVolume",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.VolumeName,
			Namespace:   opts.Namespace,
			Labels:      managedLabels(),
			Annotations: annotations,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				HTTP: &cdiv1.DataVolumeSourceHTTP{
					URL:           opts.HTTPURL,
					SecretRef:     opts.HTTPSecretRef,
					CertConfigMap: opts.HTTPCertConfigMap,
					Checksum:      opts.Checksum,
				},
			},
			Storage:     storage,
			ContentType: cdiv1.DataVolumeKubeVirt,
		},
	}, nil
}

func validateHTTPDataVolume(dv *cdiv1.DataVolume, opts Options, identity Identity) error {
	if !isManaged(dv) {
		return fmt.Errorf("existing DataVolume is not owned by packer-plugin-kubevirt")
	}
	annotations := dv.GetAnnotations()
	if annotations[AnnotationSourceKind] != string(SourceHTTP) ||
		annotations[AnnotationSourceURLHash] != identity.URLHash {
		return fmt.Errorf("existing DataVolume source identity does not match iso_url")
	}
	if dv.Spec.Source == nil || dv.Spec.Source.HTTP == nil || dv.Spec.Source.HTTP.URL != opts.HTTPURL ||
		!strings.EqualFold(dv.Spec.Source.HTTP.Checksum, opts.Checksum) ||
		dv.Spec.Source.HTTP.SecretRef != opts.HTTPSecretRef ||
		dv.Spec.Source.HTTP.CertConfigMap != opts.HTTPCertConfigMap {
		return fmt.Errorf("existing DataVolume HTTP source does not match requested URL, checksum, or credential references")
	}
	if dv.Spec.Storage == nil {
		return fmt.Errorf("existing DataVolume has no storage specification")
	}
	requested, err := resource.ParseQuantity(opts.StorageSize)
	if err != nil {
		return err
	}
	actual := dv.Spec.Storage.Resources.Requests[corev1.ResourceStorage]
	if actual.Cmp(requested) < 0 {
		return fmt.Errorf("existing DataVolume storage size %s is smaller than requested %s", actual.String(), requested.String())
	}
	if opts.StorageClass != "" &&
		(dv.Spec.Storage.StorageClassName == nil || *dv.Spec.Storage.StorageClassName != opts.StorageClass) {
		return fmt.Errorf("existing DataVolume storage class does not match %q", opts.StorageClass)
	}
	return nil
}

func managedLabels() map[string]string {
	return map[string]string{
		LabelManagedBy: ManagedByValue,
		LabelStaging:   "true",
	}
}

func managedAnnotations(identity Identity, state string) map[string]string {
	annotations := map[string]string{
		AnnotationManaged:     "true",
		AnnotationSourceKind:  string(identity.Kind),
		AnnotationImportState: state,
	}
	if identity.URLHash != "" {
		annotations[AnnotationSourceURLHash] = identity.URLHash
	}
	return annotations
}

func isManaged(dv *cdiv1.DataVolume) bool {
	return dv != nil && dv.Annotations[AnnotationManaged] == "true" &&
		dv.Labels[LabelManagedBy] == ManagedByValue
}

func (m *Manager) patchImportState(ctx context.Context, namespace, name, state string) error {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{AnnotationImportState: state},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal import-state patch: %w", err)
	}
	_, err = m.CDI.CdiV1beta1().DataVolumes(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func dataVolumeConditionMessage(dv *cdiv1.DataVolume) string {
	var messages []string
	for _, condition := range dv.Status.Conditions {
		if condition.Message != "" {
			messages = append(messages, condition.Message)
		} else if condition.Reason != "" {
			messages = append(messages, condition.Reason)
		}
	}
	if len(messages) == 0 {
		return "no condition message"
	}
	return strings.Join(messages, "; ")
}
