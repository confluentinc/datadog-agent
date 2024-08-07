// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package settings

import (
	"fmt"

	"github.com/DataDog/datadog-agent/cmd/agent/common"
	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/config/settings"
)

// DsdStatsRuntimeSetting wraps operations to change the collection of dogstatsd stats at runtime.
type DsdStatsRuntimeSetting string

// Description returns the runtime setting's description
func (s DsdStatsRuntimeSetting) Description() string {
	return "Enable/disable the dogstatsd debug stats. Possible values: true, false"
}

// Hidden returns whether or not this setting is hidden from the list of runtime settings
func (s DsdStatsRuntimeSetting) Hidden() bool {
	return false
}

// Name returns the name of the runtime setting
func (s DsdStatsRuntimeSetting) Name() string {
	return string(s)
}

// Get returns the current value of the runtime setting
func (s DsdStatsRuntimeSetting) Get() (interface{}, error) {
	return common.DSD.Debug.Enabled.Load(), nil
}

// Set changes the value of the runtime setting
func (s DsdStatsRuntimeSetting) Set(v interface{}) error {
	var newValue bool
	var err error

	if newValue, err = settings.GetBool(v); err != nil {
		return fmt.Errorf("DsdStatsRuntimeSetting: %v", err)
	}

	if newValue {
		common.DSD.EnableMetricsStats()
	} else {
		common.DSD.DisableMetricsStats()
	}

	config.Datadog.Set("dogstatsd_metrics_stats_enable", newValue)
	return nil
}
