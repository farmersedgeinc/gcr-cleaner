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
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"os/exec"

	"github.com/gammazero/workerpool"
	gcrauthn "github.com/google/go-containerregistry/pkg/authn"
	gcrname "github.com/google/go-containerregistry/pkg/name"
	gcrgoogle "github.com/google/go-containerregistry/pkg/v1/google"
	gcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
)

var keep, _ = strconv.Atoi(getenv("CLEANER_KEEP_AMOUNT", "5"))
var	repo = getenv("GCR_BASE_REPO", "")
var	exPath = getenv("CLEANER_EXCEPTION_FILE", "/config/exceptions.json")
var exists = struct{}{} // for implementing set-like functionality

// Cleaner is a gcr cleaner.
type Cleaner struct {
	auther      gcrauthn.Authenticator
	concurrency int
	repoExcept  map[string]struct{}
	tagExcept   map[string]struct{}
}

// NewCleaner creates a new GCR cleaner with the given token provider and
// concurrency.
func NewCleaner(auther gcrauthn.Authenticator, c int) (*Cleaner, error) {
	repoExcept, tagExcept := fetchExceptions()
	return &Cleaner{
		auther:      auther,
		concurrency: c,
		repoExcept:  repoExcept,
		tagExcept:   tagExcept,
	}, nil
}

// Clean deletes old images from GCR that are untagged and older than "since".
func (c *Cleaner) Clean() ([]string, error) {
	var deleted []string
	var errStrings []string

	gcrbase, err := gcrname.NewRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get base repo %s: %w", repo, err)
	}

	repos, err := gcrgoogle.List(gcrbase, gcrgoogle.WithAuth(c.auther))
	if err != nil {
		return nil, fmt.Errorf("failed to list child repos %s: %w", repo, err)
	}

	log.Printf("deleting refs for %s, keeping %d tags per image\n", repo, keep)

	for _, r := range(repos.Children) {
		name := fmt.Sprintf("%s/%s", repo, r)

		gcrrepo, err := gcrname.NewRepository(name)
		if err != nil {
			errStrings = append(errStrings, fmt.Sprintf("failed to get child repo %s: %w", repo, err.Error()))
			continue
		}

		tags, err := gcrgoogle.List(gcrrepo, gcrgoogle.WithAuth(c.auther))
		if err != nil {
			errStrings = append(errStrings, fmt.Sprintf("failed to list tags for child repo %s: %w", repo, err.Error()))
			continue
		}

		// Create a worker pool for parallel deletion
		pool := workerpool.New(c.concurrency)

		var deletedLock sync.Mutex
		var errs = make(map[string]error)
		var errsLock sync.RWMutex

		var keeping = c.tagExcept
		control := max(len(tags.Tags)-keep, 0)
		if _, ok := c.repoExcept[name]; ok {
			fmt.Printf("Only deleting untagged manifests for exception repo: %s\n", name)
			control = 0
		}
		for t := len(tags.Tags)-1; t >= control; t-- {
			tagName := fmt.Sprintf("%s:%s", name, tags.Tags[t])
			keeping[tagName] = exists
		}

		for k, m := range tags.Manifests {
			if c.shouldDelete(name, m, keeping) {
				// Deletes all tags before deleting the image
				for _, tag := range m.Tags {
					tagged := name + ":" + tag
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
			for _, v := range errs {
				errStrings = append(errStrings, v.Error())
			}
		}
	}

	if len(errStrings) > 0 {
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

// shouldDelete returns true if the manifest has no tags or isn't in use by images being kept
func (c *Cleaner) shouldDelete(n string, m gcrgoogle.ManifestInfo, keeping map[string]struct{}) bool {
	if len(m.Tags) > 0 {
		for _, t := range(m.Tags) {
			name := fmt.Sprintf("%s:%s", n, t)
			if _, ok := keeping[name]; ok {
				// cannot delete manifest since it's used by images being kept
				fmt.Printf("%s kept: %+v\n", n, m)
				return false
			}
		}
	}
	fmt.Printf("%s deleted: %+v\n", n, m)
	return true
}

// fetches in-use tags across all clusters in kube config
func fetchExceptions() (map[string]struct{}, map[string]struct{}) {
	repoExceptions := make(map[string]struct{})
	tagExceptions := make(map[string]struct{})

	out, err := exec.Command("/bin/bash", "-c", `for ctx in $(kubectl config get-contexts -o name)
	do
	  { kubectl --context $ctx get cj --all-namespaces -o jsonpath="{..image}" & kubectl --context $ctx get job --all-namespaces -o jsonpath="{..image}" & kubectl --context $ctx get po --all-namespaces -o jsonpath="{..image}"; }
	done |  tr -s '[[:space:]]' ',' | sort |  uniq;`).Output()
	if err != nil {
		log.Fatalf(fmt.Sprintf("failed to retrieve in-use images across clusters:\n%s", err.Error()))
	} else {
		tags := strings.SplitAfter(string(out), ",")
		for _, tag := range tags {
			tagExceptions[tag] = exists
		}
	}

	exFile, _ := ioutil.ReadFile(exPath)
	result := make(map[string][]string)
	json.Unmarshal([]byte(exFile), &result)
	for _, r := range(result["repo"]) {
		name := fmt.Sprintf("%s/%s", repo, r)
		repoExceptions[name] = exists
	}
	for _, t := range(result["tag"]) {
		name := fmt.Sprintf("%s/%s", repo, t)
		tagExceptions[name] = exists
	}

	return repoExceptions, tagExceptions
}

// for repos with size less than or equal to keep amount
func max(x, y int) int {
 if x > y {
   return x
 }
 return y
}

// get environment variables with default
func getenv(key, fallback string) string {
    value := os.Getenv(key)
    if len(value) == 0 {
        return fallback
    }
    return value
}
