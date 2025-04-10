// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux
// +build linux

package cgroups

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSelfReader(t *testing.T) {
	selfReader, err := NewSelfReader("./testdata/self-reader", true)
	assert.NoError(t, err)

	assert.NotNil(t, selfReader.GetCgroup(SelfCgroupIdentifier))
}
