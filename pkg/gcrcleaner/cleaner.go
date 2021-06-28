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

// Cleaner is a gcr cleaner.
type Cleaner struct {
	auther          gcrauthn.Authenticator
	concurrency     int
	repoExcept      map[string]bool
	tagExcept       map[string]bool
	globalTagExcept map[string]bool
}

// NewCleaner creates a new GCR cleaner with the given token provider and
// concurrency.
func NewCleaner(auther gcrauthn.Authenticator, c int) (*Cleaner, error) {
	repoExcept, tagExcept, globalTagExcept := fetchExceptions()
	return &Cleaner{
		auther:          auther,
		concurrency:     c,
		repoExcept:      repoExcept,
		tagExcept:       tagExcept,
		globalTagExcept: globalTagExcept,
	}, nil
}

// Clean deletes old images from GCR that are untagged and older than "since".
func (c *Cleaner) Clean(dry bool) ([]string, error) {
	var status []string
	var errStrings []string

	gcrbase, err := gcrname.NewRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get base repo %s: %w", repo, err)
	}

	repos, err := gcrgoogle.List(gcrbase, gcrgoogle.WithAuth(c.auther))
	if err != nil {
		return nil, fmt.Errorf("failed to list child repos %s: %w", repo, err)
	}

	if dry {
		log.Printf("Performing dry run simulating clean for %s, with at least %d tags unflagged per repo\n", repo, keep)
	} else {
		log.Printf("Deleting refs for %s, keeping at least %d tags per repo\n", repo, keep)
	}

	for _, r := range(repos.Children) {
		name := fmt.Sprintf("%s/%s", repo, r)
		size := int64(0)
		del := 0

		gcrrepo, err := gcrname.NewRepository(name)
		if err != nil {
			errStrings = append(errStrings, fmt.Sprintf("Failed to get child repo %s: %w", name, err.Error()))
			continue
		}

		tags, err := gcrgoogle.List(gcrrepo, gcrgoogle.WithAuth(c.auther))
		if err != nil {
			errStrings = append(errStrings, fmt.Sprintf("Failed to list tags for child repo %s: %w", name, err.Error()))
			continue
		}

		// Create a worker pool for parallel deletion
		pool := workerpool.New(c.concurrency)

		var deletedLock sync.Mutex
		var errs = make(map[string]error)
		var errsLock sync.RWMutex

		var keeping = c.tagExcept
		control := max(len(tags.Tags)-keep, 0)
		if c.repoExcept[name] {
			if dry {
				log.Printf("Only flagging untagged manifests for exception repo: %s", name)
			} else {
				log.Printf("Only deleting untagged manifests for exception repo: %s", name)
			}
			control = 0
		}
		for t := len(tags.Tags)-1; t >= control; t-- {
			tagName := fmt.Sprintf("%s:%s", name, tags.Tags[t])
			if c.globalTagExcept[tags.Tags[t]] || c.tagExcept[tagName] {
				//If it's a tag exception we want to keep it but not count it towards the total
				control = max(control-1, 0)
			}
			keeping[tagName] = true
		}

		for k, m := range tags.Manifests {
			if c.shouldDelete(name, m, keeping, &size) {
				if dry {
					del += 1
					log.Printf("%s would delete manifest %s: %+v", name, k, m)
					continue
				}
				// Deletes all tags before deleting the image
				for _, tag := range m.Tags {
					tagged := name + ":" + tag
					c.deleteOne(tagged)
				}
				ref := name + "@" + k
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
					del += 1
					deletedLock.Unlock()
				})
			}
		}

		// Wait for everything to finish
		if !dry {
			pool.StopWait()

			// Aggregate any errors
			if len(errs) > 0 {
				for _, v := range errs {
					errStrings = append(errStrings, v.Error())
				}
			} else {
				// Add status update for child repo
				status = append(status, fmt.Sprintf("%s: %d manifests deleted, %d manifests kept, remaining size %s", name, del, len(tags.Manifests)-del, getSize(size)))
			}
		} else {
			status = append(status, fmt.Sprintf("%s: %d manifests would be deleted, %d manifests would be kept, would be remaining size %s", name, del, len(tags.Manifests)-del, getSize(size)))
		}
	}

	if len(errStrings) > 0 {
		if len(errStrings) == 1 {
			return status, fmt.Errorf(errStrings[0])
		}

		return status, fmt.Errorf("%d errors occurred: %s",
			len(errStrings), strings.Join(errStrings, ", "))
	}
	return status, nil
}

// deleteOne deletes a single repo ref using the supplied auth.
func (c *Cleaner) deleteOne(ref string) error {
	name, err := gcrname.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("Failed to parse reference %s: %w", ref, err)
	}

	if err := gcrremote.Delete(name, gcrremote.WithAuth(c.auther)); err != nil {
		return fmt.Errorf("Failed to delete %s: %w", name, err)
	}

	return nil
}

// shouldDelete returns true if the manifest has no tags or isn't in use by images being kept
func (c *Cleaner) shouldDelete(n string, m gcrgoogle.ManifestInfo, keeping map[string]bool, total *int64) bool {
	if len(m.Tags) > 0 {
		for _, t := range(m.Tags) {
			name := fmt.Sprintf("%s:%s", n, t)
			if keeping[name] {
				// cannot delete manifest since it's used by images being kept
				*total += int64(m.Size)
				return false
			}
		}
	}
	return true
}

// fetches in-use tags across all clusters in kube config
func fetchExceptions() (map[string]bool, map[string]bool, map[string]bool) {
	repoExceptions := make(map[string]bool)
	tagExceptions := make(map[string]bool)
	globalTagExceptions := make(map[string]bool)

	out, err := exec.Command("/bin/bash", "-c", `for ctx in $(kubectl config get-contexts -o name)
	do
	  { kubectl --context $ctx get cj --all-namespaces -o jsonpath="{..image}" & kubectl --context $ctx get job --all-namespaces -o jsonpath="{..image}" & kubectl --context $ctx get po --all-namespaces -o jsonpath="{..image}"; }
	done |  tr -s '[[:space:]]' ',' | sort |  uniq;`).Output()
	if err != nil {
		log.Fatalf(fmt.Sprintf("Failed to retrieve in-use images across clusters: %s", err.Error()))
	} else {
		tags := strings.SplitAfter(string(out), ",")
		for _, tag := range tags {
			tagExceptions[tag] = true
		}
	}

	exFile, _ := ioutil.ReadFile(exPath)
	result := make(map[string][]string)
	parseErr := json.Unmarshal([]byte(exFile), &result)
	if parseErr != nil {
		log.Fatalf(fmt.Sprintf("Failed to parse JSON exceptions file: %s", parseErr.Error()))
	}
	for _, r := range(result["repo"]) {
		name := fmt.Sprintf("%s/%s", repo, r)
		repoExceptions[name] = true
	}
	for _, t := range(result["tag"]) {
		name := fmt.Sprintf("%s/%s", repo, t)
		tagExceptions[name] = true
	}
	for _, t := range(result["globalTag"]) {
		globalTagExceptions[t] = true
	}

	return repoExceptions, tagExceptions, globalTagExceptions
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

// get human readable size
func getSize(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}