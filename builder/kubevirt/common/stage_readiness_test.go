// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package common

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetes "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	fakecdiclient "kubevirt.io/client-go/containerizeddataimporter/fake"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const (
	sampleDigest = "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce" +
		"47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"
	otherDigest = "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a" +
		"2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"
)

// fakeReadyClient satisfies MediaReadyClient with in-memory fake clientsets.
type fakeReadyClient struct {
	cdi  cdiclient.Interface
	kube kubernetes.Interface
}

func (f fakeReadyClient) CdiClient() cdiclient.Interface { return f.cdi }

func (f fakeReadyClient) CoreV1() corev1client.CoreV1Interface { return f.kube.CoreV1() }

func newReadyClient(objs ...runtime.Object) fakeReadyClient {
	var dvs []runtime.Object
	var pvcs []runtime.Object
	for _, o := range objs {
		switch o.(type) {
		case *cdiv1.DataVolume:
			dvs = append(dvs, o)
		case *corev1.PersistentVolumeClaim:
			pvcs = append(pvcs, o)
		}
	}
	return fakeReadyClient{
		cdi:  fakecdiclient.NewSimpleClientset(dvs...),
		kube: k8sfake.NewSimpleClientset(pvcs...),
	}
}

func dataVolume(name string, phase cdiv1.DataVolumePhase, labels map[string]string) *cdiv1.DataVolume {
	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels},
		Status:     cdiv1.DataVolumeStatus{Phase: phase},
	}
}

func pvc(name string, annotations map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Annotations: annotations},
	}
}

func shortWait() MediaReadyOptions { return MediaReadyOptions{Timeout: 150 * time.Millisecond} }

func TestNormalizeSHA512(t *testing.T) {
	if got, err := NormalizeSHA512(sampleDigest); err != nil || got != sampleDigest {
		t.Fatalf("bare digest: got %q err %v", got, err)
	}
	if got, err := NormalizeSHA512("SHA512:" + strings.ToUpper(sampleDigest)); err != nil || got != sampleDigest {
		t.Fatalf("prefixed/upper digest: got %q err %v", got, err)
	}
	if _, err := NormalizeSHA512("deadbeef"); err == nil {
		t.Error("expected error for a too-short digest")
	}
	if _, err := NormalizeSHA512("z" + sampleDigest[1:]); err == nil {
		t.Error("expected error for a non-hex digest")
	}
}

func TestIsStageManaged(t *testing.T) {
	cases := []struct {
		name string
		dv   *cdiv1.DataVolume
		pvc  *corev1.PersistentVolumeClaim
		want bool
	}{
		{"stage label", dataVolume("v", cdiv1.Succeeded, map[string]string{StageLabel: "true"}), nil, true},
		{"stage-tag label", dataVolume("v", cdiv1.Succeeded, map[string]string{StageTagLabel: "abc"}), nil, true},
		{"dv annotation", &cdiv1.DataVolume{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{StageBytesAnnotation: "1"}}}, nil, true},
		{"pvc annotation", dataVolume("v", cdiv1.Succeeded, nil), pvc("v", map[string]string{StageCompleteAnnotation: "true"}), true},
		{"plain volume", dataVolume("v", cdiv1.Succeeded, nil), pvc("v", nil), false},
	}
	for _, tc := range cases {
		if got := isStageManaged(tc.dv, tc.pvc); got != tc.want {
			t.Errorf("%s: isStageManaged = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWaitUntilMediaReadyFailsFastWhenAbsent(t *testing.T) {
	client := newReadyClient()
	err := WaitUntilMediaReady(context.Background(), client, testNamespace, "missing", shortWait())
	if err == nil || !strings.Contains(err.Error(), "resolve DataVolume") {
		t.Fatalf("expected a resolve error for a missing DataVolume, got %v", err)
	}
}

func TestWaitUntilMediaReadyNonStagePhase(t *testing.T) {
	ready := newReadyClient(dataVolume("ok", cdiv1.Succeeded, nil))
	if err := WaitUntilMediaReady(context.Background(), ready, testNamespace, "ok", shortWait()); err != nil {
		t.Fatalf("non-stage Succeeded volume should be ready: %v", err)
	}

	failed := newReadyClient(dataVolume("bad", cdiv1.Failed, nil))
	if err := WaitUntilMediaReady(context.Background(), failed, testNamespace, "bad", shortWait()); err == nil {
		t.Fatal("non-stage Failed volume should error")
	}
}

func TestWaitUntilMediaReadyStageGate(t *testing.T) {
	// A stage-managed DataVolume that is CDI Succeeded (bound) but NOT
	// stage-complete must NOT be treated as ready — this is the core gate.
	incomplete := newReadyClient(
		dataVolume("stage", cdiv1.Succeeded, map[string]string{StageLabel: "true"}),
		pvc("stage", nil),
	)
	err := WaitUntilMediaReady(context.Background(), incomplete, testNamespace, "stage", shortWait())
	if err == nil || !strings.Contains(err.Error(), "not stage-complete") {
		t.Fatalf("bound-but-unmarked stage volume must fail closed, got %v", err)
	}

	// Once stage-complete is stamped, it is ready.
	complete := newReadyClient(
		dataVolume("stage", cdiv1.Succeeded, map[string]string{StageLabel: "true"}),
		pvc("stage", map[string]string{StageCompleteAnnotation: "true"}),
	)
	if err := WaitUntilMediaReady(context.Background(), complete, testNamespace, "stage", shortWait()); err != nil {
		t.Fatalf("stage-complete volume should be ready: %v", err)
	}
}

func TestWaitUntilMediaReadyDigestPin(t *testing.T) {
	match := newReadyClient(
		dataVolume("stage", cdiv1.Succeeded, map[string]string{StageLabel: "true"}),
		pvc("stage", map[string]string{StageCompleteAnnotation: "true", StageContentSHA512Annotation: sampleDigest}),
	)
	opts := shortWait()
	opts.ExpectedSHA512 = sampleDigest
	if err := WaitUntilMediaReady(context.Background(), match, testNamespace, "stage", opts); err != nil {
		t.Fatalf("matching digest should pass: %v", err)
	}

	mismatch := newReadyClient(
		dataVolume("stage", cdiv1.Succeeded, map[string]string{StageLabel: "true"}),
		pvc("stage", map[string]string{StageCompleteAnnotation: "true", StageContentSHA512Annotation: otherDigest}),
	)
	opts = shortWait()
	opts.ExpectedSHA512 = sampleDigest
	if err := WaitUntilMediaReady(context.Background(), mismatch, testNamespace, "stage", opts); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mismatched digest must fail closed, got %v", err)
	}

	missing := newReadyClient(
		dataVolume("stage", cdiv1.Succeeded, map[string]string{StageLabel: "true"}),
		pvc("stage", map[string]string{StageCompleteAnnotation: "true"}),
	)
	opts = shortWait()
	opts.ExpectedSHA512 = sampleDigest
	if err := WaitUntilMediaReady(context.Background(), missing, testNamespace, "stage", opts); err == nil {
		t.Fatal("expected an error when a digest pin is set but no marker digest exists")
	}
}
