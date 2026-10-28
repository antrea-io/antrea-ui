// Copyright 2026 Antrea Authors.
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

package v1

import "time"

// SupportBundleRequest is the optional body of POST /api/v1/supportbundle.
type SupportBundleRequest struct {
	// Since is a Go duration (e.g. "1h"). When set, only rotated log files last written within that
	// window are collected (the current log file is always included). It is forwarded unchanged
	// to every source.
	Since string `json:"since,omitempty"`
}

// SupportBundleStatus is the lifecycle state of a support bundle. The values match the ones used by
// Antrea's own SupportBundle API. They are also the phases of a SourceBundle.
type SupportBundleStatus string

const (
	SupportBundleStatusCollecting SupportBundleStatus = "Collecting"
	SupportBundleStatusCollected  SupportBundleStatus = "Collected"
	SupportBundleStatusFailed     SupportBundleStatus = "Failed"
)

// SupportBundleSourceKindPlugin and SupportBundleSourceKindExtra are the values of
// SupportBundleSourceStatus.Kind: a source declared by a plugin manifest, or one configured by the
// operator (supportBundle.extraSources).
const (
	SupportBundleSourceKindPlugin = "plugin"
	SupportBundleSourceKindExtra  = "extra"
)

// SupportBundle describes one support bundle, as antrea-ui returns it.
type SupportBundle struct {
	ID        string              `json:"id"`
	Status    SupportBundleStatus `json:"status"`
	CreatedBy string              `json:"createdBy,omitempty"`
	CreatedAt time.Time           `json:"createdAt"`
	ExpiresAt time.Time           `json:"expiresAt"`
	// Size is the size in bytes of the downloadable tarball, once collected.
	Size int64 `json:"size,omitempty"`
	// Error explains why the bundle failed.
	Error string `json:"error,omitempty"`
	// Sources reports the outcome for every secondary source.
	Sources []SupportBundleSourceStatus `json:"sources,omitempty"`
}

// SupportBundleSourceStatus is the outcome of collecting one secondary source. A failed source does
// not fail the aggregate bundle, unless it exceeds the storage budget.
type SupportBundleSourceStatus struct {
	Name   string              `json:"name"`
	Kind   string              `json:"kind"`
	Status SupportBundleStatus `json:"status"`
	Error  string              `json:"error,omitempty"`
	// Size is the size in bytes of the tarball downloaded from the source.
	Size int64 `json:"size,omitempty"`
}

// SupportBundleList is the response of GET /api/v1/supportbundle.
type SupportBundleList struct {
	Items []SupportBundle `json:"items"`
}

// SourceBundle is a bundle of a support bundle source, in the source protocol (see
// docs/supportbundle.md): it is the body of the POST antrea-ui sends to the source (with only
// Metadata.GenerateName and Spec set), and of the source's responses. It has the shape of a
// Kubernetes object, so that an aggregated API can serve it as a regular resource.
type SourceBundle struct {
	Metadata SourceBundleMetadata `json:"metadata,omitzero"`
	Spec     SourceBundleSpec     `json:"spec,omitzero"`
	Status   SourceBundleStatus   `json:"status,omitzero"`
}

// SourceBundleGenerateName is the metadata.generateName of the POST antrea-ui sends to a source.
const SourceBundleGenerateName = "antrea-ui-"

// SourceBundleMetadata identifies a SourceBundle.
type SourceBundleMetadata struct {
	// Name is the bundle's ID, which the source picks.
	Name string `json:"name,omitempty"`
	// GenerateName is only set in the POST, to SourceBundleGenerateName, for a source whose
	// storage requires a name or a generateName, as the generic apiserver registry does. The
	// source may ignore it.
	GenerateName string `json:"generateName,omitempty"`
}

// SourceBundleSpec describes what to collect.
type SourceBundleSpec struct {
	// Since is SupportBundleRequest.Since, forwarded unchanged.
	Since string `json:"since,omitempty"`
}

// SourceBundleStatus is the state of a SourceBundle.
type SourceBundleStatus struct {
	Phase SupportBundleStatus `json:"phase,omitempty"`
	// Message explains why the bundle failed.
	Message string `json:"message,omitempty"`
}

// SupportBundleSourceSpec declares a support bundle source. How the source is reached is a union,
// with APIServer as its only variant. A declaration with no variant antrea-ui recognizes only
// skips the source: the plugin still loads.
type SupportBundleSourceSpec struct {
	// APIServer is a source served through the Kubernetes apiserver, typically by an
	// APIService.
	APIServer *APIServerSourceSpec `json:"apiServer,omitempty"`
	// MaxConcurrent caps how many antrea-ui collections are outstanding against the source at
	// once, each from its POST until its DELETE has returned. A collection waits for a slot,
	// within the collection timeout. 0 means no limit. A source that can only hold one bundle at a
	// time (e.g. because it uses the same ID for every bundle) sets 1.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// APIServerSourceSpec is a support bundle source reached through the Kubernetes apiserver.
type APIServerSourceSpec struct {
	// Path is the base path of the source, which must be of the form /apis/<group>/<version>.
	// antrea-ui appends /supportbundles to it.
	Path string `json:"path"`
}
