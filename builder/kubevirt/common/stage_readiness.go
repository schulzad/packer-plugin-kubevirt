// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package common

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	corev1 "k8s.io/api/core/v1"
	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

// harvester-image-tools `stage-iso` is the air-gapped substitute for `virtctl
// image-upload`: it creates a blank, UNGUARDED Block CDI DataVolume UP FRONT and
// RAW-populates it OUT OF BAND (resumable, sha512-gated dd-over-exec), then
// stamps a completion marker on the bound PVC. The load-bearing consequence for
// this plugin: such a DataVolume reaches CDI status.phase==Succeeded (bound)
// BEFORE — or entirely WITHOUT — its bytes being present, because
// source:{blank:{}} succeeds on PROVISION, not on POPULATION. Readiness for a
// stage-managed volume MUST therefore gate on the completion annotation, never
// on CDI phase alone, or the builder can boot/attach a blank or half-staged
// CD-ROM. These are the exact keys stage-iso writes.
const (
	// StageAnnotationPrefix marks any harvester-image-tools stage annotation.
	StageAnnotationPrefix = "harvester-image-tools/stage-"
	// StageCompleteAnnotation is "true" only after the bytes are fully staged
	// AND sha512-verified by stage-iso's in-pod gate.
	StageCompleteAnnotation = "harvester-image-tools/stage-complete"
	// StageContentSHA512Annotation is the verified ISO byte digest (SHA-512 hex).
	StageContentSHA512Annotation = "harvester-image-tools/stage-content-sha512"
	// StageBytesAnnotation is the staged byte count.
	StageBytesAnnotation = "harvester-image-tools/stage-bytes"

	// StageLabel ("true") and StageTagLabel identify a stage-iso DataVolume from
	// cluster state alone, so the completion gate is applied even to a plain
	// iso_volume_name / data_volume reference that happens to be a stage-iso
	// artifact.
	StageLabel    = "stage"
	StageTagLabel = "stage-tag"
)

// DefaultMediaReadyTimeout bounds the wait for a media DataVolume to become
// ready (CDI-populated, or stage-complete for a stage-managed volume).
const DefaultMediaReadyTimeout = time.Hour

// MediaReadyClient is the subset of kubecli.KubevirtClient the readiness gate
// needs: the CDI client for DataVolumes and core/v1 for the bound PVC that
// carries the stage-iso completion marker. kubecli.KubevirtClient satisfies it.
type MediaReadyClient interface {
	CdiClient() cdiclient.Interface
	CoreV1() corev1client.CoreV1Interface
}

// MediaReadyOptions tunes a single readiness wait.
type MediaReadyOptions struct {
	// ExpectedSHA512 optionally pins the media's content digest. When set (bare
	// lowercase SHA-512 hex) and the volume is stage-managed, it must equal the
	// harvester-image-tools/stage-content-sha512 marker or the wait fails closed.
	ExpectedSHA512 string
	// Timeout bounds the wait; DefaultMediaReadyTimeout is used when <= 0.
	Timeout time.Duration
	// Progress, when set, receives human-readable status updates.
	Progress func(format string, args ...any)
}

// WaitUntilMediaReady blocks until a referenced media DataVolume is safe for the
// temporary build VM to attach, then returns. It fails fast if the DataVolume is
// absent.
//
// For a harvester-image-tools stage-iso DataVolume (blank Block, raw-populated
// out of band), readiness gates on the stage-complete marker on the bound PVC —
// NEVER on CDI status.phase, which reports Succeeded once the empty volume binds,
// before its bytes are staged. For any other DataVolume it falls back to the
// ordinary CDI-phase readiness (Succeeded), matching iso_volume_name's existing
// contract. The plugin only CONSUMES the volume; it never creates, imports,
// mutates, adopts, or deletes it.
func WaitUntilMediaReady(ctx context.Context, client MediaReadyClient, namespace, name string, opts MediaReadyOptions) error {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultMediaReadyTimeout
	}
	dvClient := client.CdiClient().CdiV1beta1().DataVolumes(namespace)
	pvcClient := client.CoreV1().PersistentVolumeClaims(namespace)

	// Fail fast when the volume the pipeline promised does not exist, instead of
	// stalling on a missing volume at VM create.
	dv, err := dvClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("resolve DataVolume %s/%s: %w", namespace, name, err)
	}

	// A stage-iso DataVolume shares its name with its bound PVC (CDI names the
	// PVC after the DataVolume), so the completion marker is read off the PVC of
	// the same name.
	pvc, pvcErr := pvcClient.Get(ctx, name, metav1.GetOptions{})
	if pvcErr != nil && !apierrors.IsNotFound(pvcErr) {
		return fmt.Errorf("resolve PVC %s/%s: %w", namespace, name, pvcErr)
	}
	var boundPVC *corev1.PersistentVolumeClaim
	if pvcErr == nil {
		boundPVC = pvc
	}

	if !isStageManaged(dv, boundPVC) {
		return waitForDataVolumePhase(ctx, client.CdiClient(), namespace, name, timeout, opts.Progress)
	}

	return waitForStageComplete(ctx, pvcClient, namespace, name, timeout, opts)
}

// waitForStageComplete blocks until the bound PVC is annotated stage-complete,
// then (optionally) pins the staged content digest.
func waitForStageComplete(
	ctx context.Context,
	pvcClient corev1client.PersistentVolumeClaimInterface,
	namespace, name string,
	timeout time.Duration,
	opts MediaReadyOptions,
) error {
	reportProgress(opts.Progress, "Waiting for stage-iso completion marker on %s/%s (never trusting CDI phase for a stage-managed volume)...", namespace, name)

	var complete *corev1.PersistentVolumeClaim
	var lastReport string
	var lastReportedAt time.Time
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := pvcClient.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			throttledProgress(opts.Progress, &lastReport, &lastReportedAt, "stage-managed DataVolume %s/%s not bound yet", namespace, name)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if pvc.Annotations[StageCompleteAnnotation] != "true" {
			throttledProgress(opts.Progress, &lastReport, &lastReportedAt, "stage-managed DataVolume %s/%s is bound but not stage-complete (bytes still staging)", namespace, name)
			return false, nil
		}
		complete = pvc
		return true, nil
	})
	if err != nil {
		return fmt.Errorf(
			"DataVolume %s/%s is present but not stage-complete (bytes not fully staged/verified); "+
				"a stage-iso push may be incomplete or in-flight — do not boot a blank/partial CD-ROM, "+
				"check `harvester-image stage-iso`: %w",
			namespace, name, err)
	}

	if opts.ExpectedSHA512 != "" {
		got := strings.ToLower(strings.TrimSpace(complete.Annotations[StageContentSHA512Annotation]))
		want := strings.ToLower(strings.TrimSpace(opts.ExpectedSHA512))
		if got == "" {
			return fmt.Errorf(
				"DataVolume %s/%s is stage-complete but carries no %s to verify against the expected digest",
				namespace, name, StageContentSHA512Annotation)
		}
		if got != want {
			return fmt.Errorf(
				"DataVolume %s/%s content digest mismatch: %s=%s does not equal expected %s",
				namespace, name, StageContentSHA512Annotation, got, want)
		}
		reportProgress(opts.Progress, "Verified stage-iso content digest for %s/%s.", namespace, name)
	}
	return nil
}

// waitForDataVolumePhase is the non-stage fallback: an ordinary existing/managed
// DataVolume is ready when CDI reports Succeeded, and fails fast on Failed.
func waitForDataVolumePhase(
	ctx context.Context,
	cdi cdiclient.Interface,
	namespace, name string,
	timeout time.Duration,
	progress func(format string, args ...any),
) error {
	dvClient := cdi.CdiV1beta1().DataVolumes(namespace)
	var lastReport string
	var lastReportedAt time.Time
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		dv, err := dvClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch dv.Status.Phase {
		case cdiv1.Succeeded:
			return true, nil
		case cdiv1.Failed:
			return false, fmt.Errorf("DataVolume %s/%s reported a Failed phase", namespace, name)
		default:
			report := fmt.Sprintf("phase=%s", dv.Status.Phase)
			if dv.Status.Progress != "" {
				report += fmt.Sprintf(" progress=%s", dv.Status.Progress)
			}
			throttledProgress(progress, &lastReport, &lastReportedAt, "DataVolume %s/%s not ready: %s", namespace, name, report)
			return false, nil
		}
	})
	if err != nil {
		return fmt.Errorf("wait for DataVolume %s/%s to be ready: %w", namespace, name, err)
	}
	return nil
}

func reportProgress(progress func(format string, args ...any), format string, args ...any) {
	if progress != nil {
		progress(format, args...)
	}
}

func throttledProgress(progress func(format string, args ...any), lastReport *string, lastReportedAt *time.Time, format string, args ...any) {
	if progress == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if msg == *lastReport && time.Since(*lastReportedAt) < time.Minute {
		return
	}
	progress("%s", msg)
	*lastReport = msg
	*lastReportedAt = time.Now()
}

// NormalizeSHA512 validates and canonicalizes an expected SHA-512 digest,
// accepting either a bare hex string or a "sha512:"-prefixed one and returning
// the bare, lowercase hex form used to compare against the stage marker.
func NormalizeSHA512(s string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	v = strings.TrimPrefix(v, "sha512:")
	if len(v) != 128 {
		return "", fmt.Errorf("expected a SHA-512 hex digest (128 hex characters), got %d", len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", fmt.Errorf("expected a SHA-512 hex digest: %w", err)
	}
	return v, nil
}

func isStageManaged(dv *cdiv1.DataVolume, pvc *corev1.PersistentVolumeClaim) bool {
	if dv != nil {
		if dv.Labels[StageLabel] == "true" {
			return true
		}
		if _, ok := dv.Labels[StageTagLabel]; ok {
			return true
		}
		if hasStageAnnotation(dv.Annotations) {
			return true
		}
	}
	if pvc != nil && hasStageAnnotation(pvc.Annotations) {
		return true
	}
	return false
}

func hasStageAnnotation(annotations map[string]string) bool {
	for k := range annotations {
		if strings.HasPrefix(k, StageAnnotationPrefix) {
			return true
		}
	}
	return false
}
