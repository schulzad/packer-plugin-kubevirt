// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"time"

	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso/staging"
	"kubevirt.io/client-go/kubecli"
)

func WaitUntilDataVolumeSucceeded(ctx context.Context, client kubecli.KubevirtClient, namespace, name string) error {
	_, err := staging.WaitForDataVolume(ctx, client.CdiClient(), namespace, name, time.Hour, nil)
	return err
}
