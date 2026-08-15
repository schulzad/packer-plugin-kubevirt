// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package image

import (
	"strings"
	"testing"
	"time"
)

func baseRaw() map[string]any {
	return map[string]any{
		"kube_config":       "/tmp/kubeconfig",
		"name":              "fedora-44-golden",
		"namespace":         "images",
		"source_datasource": "fedora-44",
		"disk_size":         "20Gi",
		"memory":            "4Gi",
		"preference":        "fedora",
	}
}

func TestPrepareValidCloneSource(t *testing.T) {
	var c Config
	if _, err := c.Prepare(baseRaw()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.OperatingSystemType != "linux" {
		t.Errorf("os_type default = %q, want linux", c.OperatingSystemType)
	}
	if c.SourceNamespace != "images" {
		t.Errorf("source_namespace default = %q, want images", c.SourceNamespace)
	}
	if c.BootTimeout != time.Hour {
		t.Errorf("boot_timeout default = %s, want 1h", c.BootTimeout)
	}
}

func TestPrepareShutdownDefaults(t *testing.T) {
	var c Config
	raw := baseRaw()
	raw["communicator"] = "ssh"
	raw["shutdown_command"] = "sudo shutdown -h now"
	if _, err := c.Prepare(raw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ShutdownCommand != "sudo shutdown -h now" {
		t.Errorf("shutdown_command = %q, want the configured command", c.ShutdownCommand)
	}
	if c.ShutdownTimeout != 5*time.Minute {
		t.Errorf("shutdown_timeout default = %s, want 5m", c.ShutdownTimeout)
	}
}

func TestPrepareSourceNamespaceOverride(t *testing.T) {
	var c Config
	raw := baseRaw()
	raw["source_namespace"] = "golden"
	if _, err := c.Prepare(raw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.SourceNamespace != "golden" {
		t.Errorf("source_namespace = %q, want golden", c.SourceNamespace)
	}
}

func TestPrepareErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantSub string
	}{
		{
			name:    "missing source_datasource",
			mutate:  func(r map[string]any) { delete(r, "source_datasource") },
			wantSub: "source_datasource",
		},
		{
			name:    "source collides with output",
			mutate:  func(r map[string]any) { r["source_datasource"] = "fedora-44-golden" },
			wantSub: "collide",
		},
		{
			name:    "source collides with rootdisk",
			mutate:  func(r map[string]any) { r["source_datasource"] = "fedora-44-golden-rootdisk" },
			wantSub: "collide",
		},
		{
			name:    "missing disk_size",
			mutate:  func(r map[string]any) { delete(r, "disk_size") },
			wantSub: "disk_size",
		},
		{
			name:    "invalid disk_size",
			mutate:  func(r map[string]any) { r["disk_size"] = "-5Gi" },
			wantSub: "disk_size",
		},
		{
			name:    "instance_type combined with memory",
			mutate:  func(r map[string]any) { r["instance_type"] = "u1.medium" },
			wantSub: "cannot be combined",
		},
		{
			name: "no sizing at all",
			mutate: func(r map[string]any) {
				delete(r, "memory")
			},
			wantSub: "either instance_type or memory",
		},
		{
			name:    "invalid disk_interface",
			mutate:  func(r map[string]any) { r["disk_interface"] = "nvme" },
			wantSub: "disk_interface",
		},
		{
			name:    "invalid communicator",
			mutate:  func(r map[string]any) { r["communicator"] = "telnet" },
			wantSub: "communicator",
		},
		{
			name:    "invalid os_type",
			mutate:  func(r map[string]any) { r["os_type"] = "plan9" },
			wantSub: "os_type",
		},
		{
			name:    "shutdown_command without communicator",
			mutate:  func(r map[string]any) { r["shutdown_command"] = "shutdown -h now" },
			wantSub: "shutdown_command requires",
		},
		{
			name:    "negative shutdown_timeout",
			mutate:  func(r map[string]any) { r["shutdown_timeout"] = "-1m" },
			wantSub: "shutdown_timeout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := baseRaw()
			tc.mutate(raw)
			var c Config
			_, err := c.Prepare(raw)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestPrepareExtraMediaValid(t *testing.T) {
	var c Config
	raw := baseRaw()
	raw["extra_media"] = []map[string]any{{"data_volume": "cloudbase-media"}}
	if _, err := c.Prepare(raw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.ExtraMedia) != 1 || c.ExtraMedia[0].DataVolume != "cloudbase-media" {
		t.Fatalf("extra_media not decoded: %+v", c.ExtraMedia)
	}
}

func TestPrepareExtraMediaSHA512(t *testing.T) {
	var c Config
	raw := baseRaw()
	raw["extra_media"] = []map[string]any{{"data_volume": "m", "sha512": strings.ToUpper(sampleSHA512)}}
	if _, err := c.Prepare(raw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ExtraMedia[0].SHA512 != sampleSHA512 {
		t.Fatalf("sha512 not normalized: %q", c.ExtraMedia[0].SHA512)
	}
}

const sampleSHA512 = "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce" +
	"47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"

func TestPrepareExtraMediaErrors(t *testing.T) {
	cases := []struct {
		name  string
		media []map[string]any
		sub   string
	}{
		{"missing data_volume", []map[string]any{{"as": "cdrom"}}, "data_volume"},
		{"invalid as", []map[string]any{{"data_volume": "m", "as": "floppy"}}, "as must be"},
		{"invalid bus", []map[string]any{{"data_volume": "m", "bus": "nvme"}}, "bus must be"},
		{"reserved name", []map[string]any{{"data_volume": "m", "name": "rootdisk"}}, "reserved"},
		{"duplicate name", []map[string]any{{"data_volume": "a", "name": "x"}, {"data_volume": "b", "name": "x"}}, "duplicate"},
		{"invalid sha512", []map[string]any{{"data_volume": "m", "sha512": "deadbeef"}}, "sha512"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := baseRaw()
			raw["extra_media"] = tc.media
			var c Config
			_, err := c.Prepare(raw)
			if err == nil || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("expected error containing %q, got %v", tc.sub, err)
			}
		})
	}
}
