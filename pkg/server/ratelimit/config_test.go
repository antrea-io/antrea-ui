// Copyright 2023 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ratelimit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConfig(t *testing.T) {
	testCases := []struct {
		name           string
		rateStr        string
		burstSize      int
		expectedConfig *config
		expectedErr    string
	}{
		{
			name:        "invalid burst size",
			rateStr:     "1/s",
			burstSize:   -1,
			expectedErr: "burst size should be >= 0 for rate limiter",
		},
		{
			name:        "invalid rate",
			rateStr:     "abc",
			burstSize:   1,
			expectedErr: "not a valid rate string",
		},
		{
			name:      "valid",
			rateStr:   "1/s",
			burstSize: 10,
			expectedConfig: &config{
				ratePerSecond: 1.0,
				burstSize:     10,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			config, err := newConfig(tc.rateStr, tc.burstSize)
			if tc.expectedErr != "" {
				assert.EqualError(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedConfig, config)
			}
		})
	}
}

func TestRateFromStr(t *testing.T) {
	testCases := []struct {
		rateStr      string
		expectedRate float64
		expectedErr  string
	}{
		{
			rateStr:      "5/s",
			expectedRate: 5.0,
		},
		{
			rateStr:      "60/m",
			expectedRate: 1.0,
		},
		{
			rateStr:      "7200/h",
			expectedRate: 2.0,
		},
		{
			rateStr:      "0/s",
			expectedRate: 0.0,
		},
		{
			rateStr:     "abc",
			expectedErr: "not a valid rate string",
		},
		{
			rateStr:     "10",
			expectedErr: "not a valid rate string",
		},
		{
			rateStr:     "01/s",
			expectedErr: "not a valid rate string",
		},
		{
			rateStr:     strings.Repeat("9", 50) + "/h",
			expectedErr: "rate number cannot be converted to uint64",
		},
		{
			rateStr:     "100/d",
			expectedErr: "not a valid rate string",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.rateStr, func(t *testing.T) {
			rate, err := rateFromStr(tc.rateStr)
			if tc.expectedErr != "" {
				assert.ErrorContains(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedRate, rate)
			}
		})
	}
}
