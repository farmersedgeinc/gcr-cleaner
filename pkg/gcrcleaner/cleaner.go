// Copyright 2019 The GCR Cleaner Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcrcleaner

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"os/exec"

	"github.com/gammazero/workerpool"
	gcrauthn "github.com/google/go-containerregistry/pkg/authn"
	gcrname "github.com/google/go-containerregistry/pkg/name"
	gcrgoogle "github.com/google/go-containerregistry/pkg/v1/google"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
)

// Cleaner is a gcr cleaner.
type Cleaner struct {
	auther      gcrauthn.Authenticator
	concurrency int
	inuse       map
}

// NewCleaner creates a new GCR cleaner with the given token provider and
// concurrency.
func NewCleaner(auther gcrauthn.Authenticator, c int) (*Cleaner, error) {
	return &Cleaner{
		auther:      auther,
		concurrency: c,
		inuse:       fetchExisting()
	}, nil
}

// Clean deletes old images from GCR that are untagged and older than "since".
func (c *Cleaner) Clean(repo string, keep int) ([]string, error) {
	gcrrepo, err := gcrname.NewRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get repo %s: %w", repo, err)
	}

	tags, err := gcrgoogle.List(gcrrepo, gcrgoogle.WithAuth(c.auther))
	if err != nil {
		return nil, fmt.Errorf("failed to list tags for repo %s: %w", repo, err)
	}

	// Create a worker pool for parallel deletion
	pool := workerpool.New(c.concurrency)

	var deleted = make([]string, 0, len(tags.Manifests))
	var deletedLock sync.Mutex
	var errs = make(map[string]error)
	var errsLock sync.RWMutex

	for k, m := range tags.Manifests {
		if c.shouldDelete(m) {
			// Deletes all tags before deleting the image
			for _, tag := range m.Tags {
				tagged := repo + ":" + tag
				c.deleteOne(tagged)
			}
			ref := repo + "@" + k
			pool.Submit(func() {
				// Do not process if previous invocations failed. This prevents a large
				// build-up of failed requests and rate limit exceeding (e.g. bad auth).
				errsLock.RLock()
				if len(errs) > 0 {
					errsLock.RUnlock()
					return
				}
				errsLock.RUnlock()

				if err := c.deleteOne(ref); err != nil {
					cause := errors.Unwrap(err).Error()

					errsLock.Lock()
					if _, ok := errs[cause]; !ok {
						errs[cause] = err
						errsLock.Unlock()
						return
					}
					errsLock.Unlock()
				}

				deletedLock.Lock()
				deleted = append(deleted, k)
				deletedLock.Unlock()
			})
		}
	}

	// Wait for everything to finish
	pool.StopWait()

	// Aggregate any errors
	if len(errs) > 0 {
		var errStrings []string
		for _, v := range errs {
			errStrings = append(errStrings, v.Error())
		}

		if len(errStrings) == 1 {
			return nil, fmt.Errorf(errStrings[0])
		}

		return nil, fmt.Errorf("%d errors occurred: %s",
			len(errStrings), strings.Join(errStrings, ", "))
	}

	return deleted, nil
}

// deleteOne deletes a single repo ref using the supplied auth.
func (c *Cleaner) deleteOne(ref string) error {
	name, err := gcrname.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("failed to parse reference %s: %w", ref, err)
	}

	if err := gcrremote.Delete(name, gcrremote.WithAuth(c.auther)); err != nil {
		return fmt.Errorf("failed to delete %s: %w", name, err)
	}

	return nil
}

// shouldDelete returns true if the manifest has no tags and is before the
// requested time.
func (c *Cleaner) shouldDelete(m gcrgoogle.ManifestInfo) bool {
	fmt.Printf("%+v\n", m)
	// return (allow_tag || len(m.Tags) == 0) && m.Uploaded.UTC().Before(since)
	return false
}

//
func fetchExisting() map {
    existing = make(map[string]struct{})

	out, err := exec.Command("for ctx in $(kubectl config get-contexts -o name)
	do
	  { kubectl --context $ctx get cj --all-namespaces -o jsonpath=\"{..image}\" & kubectl --context $ctx get job --all-namespaces -o jsonpath=\"{..image}\" & kubectl --context $ctx get po --all-namespaces -o jsonpath=\"{..image}\"; }
	done |  tr -s '[[:space:]]' ',' | sort |  uniq;").Output()
	if err != nil {
		panic("failed to retrieve in-use images across clusters:\n%s", err)
	}
	else {
		tags := strings.SplitAfter(out, ",")
		for _, tag := range tags {
			existing[tag] = exists
    	}
    	return existing
	}
}
