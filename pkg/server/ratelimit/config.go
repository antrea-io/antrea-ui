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
	"fmt"
	"regexp"
	"strconv"
)

type config struct {
	ratePerSecond float64
	burstSize     int
}

func newConfig(rateStr string, burstSize int) (*config, error) {
	ratePerSecond, err := rateFromStr(rateStr)
	if err != nil {
		return nil, err
	}
	if burstSize < 0 {
		return nil, fmt.Errorf("burst size should be >= 0 for rate limiter")
	}
	return &config{
		ratePerSecond: ratePerSecond,
		burstSize:     burstSize,
	}, nil
}

var rateRegexp = regexp.MustCompile(`^(0|[1-9]+[0-9]*)\/([smh])$`)

// rateFromStr transforms the string representation of a rate (e.g., 1/m) to the
// number of events per second (as a float).
func rateFromStr(s string) (float64, error) {
	m := rateRegexp.FindStringSubmatch(s)
	if len(m) == 0 {
		return 0, fmt.Errorf("not a valid rate string")
	}
	r, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("rate number cannot be converted to uint64: %w", err)
	}
	switch m[2] {
	case "s":
		return float64(r), nil
	case "m":
		return float64(r) / 60., nil
	case "h":
		return float64(r) / 3600., nil
	}
	// not possible given that the regex specifies all acceptable units
	panic("unreachable")
}
