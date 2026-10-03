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

package supportbundle

import (
	"errors"
	"io"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

var (
	// ErrNotFound is returned for a bundle ID the manager does not know.
	ErrNotFound = errors.New("support bundle not found")
	// ErrNotCollected is returned by Open for a bundle that is still collecting, or failed.
	ErrNotCollected = errors.New("support bundle is not collected")
	// ErrLimitReached is returned by Create when the number of retained or collecting bundles
	// is at its cap, or when too little of the storage budget is left for a new bundle.
	ErrLimitReached = errors.New("support bundle limit reached")
	// ErrInvalidRequest is returned by Create for a request it cannot honor.
	ErrInvalidRequest = errors.New("invalid support bundle request")
)

// Manager collects support bundles and keeps them on disk until they expire or are deleted.
//
// Bundles are shared: any caller allowed to use the API can see, download and delete every bundle,
// whoever created it. Authorization is the API layer's job.
type Manager interface {
	// Create starts collecting a new bundle requested by the user named requestedBy. The
	// collection runs as antrea-ui-admin, independently of the requester's session: requestedBy
	// is only recorded, as the bundle's createdBy and for the sources' audit.
	Create(requestedBy string, request *apisv1.SupportBundleRequest) (*apisv1.SupportBundle, error)
	// List returns every bundle, oldest first.
	List() []apisv1.SupportBundle
	// Get returns the bundle with the given ID, or ErrNotFound.
	Get(id string) (*apisv1.SupportBundle, error)
	// Open returns the tarball of a collected bundle. The caller must close it.
	Open(id string) (io.ReadSeekCloser, *apisv1.SupportBundle, error)
	// Delete cancels the collection of a bundle if it is still in progress, and removes it.
	Delete(id string) error
	// Run garbage-collects expired bundles until stopCh is closed, which also cancels every
	// collection in progress.
	Run(stopCh <-chan struct{})
}
