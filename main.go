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

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"runtime"

	gcrgoogle "github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/farmersedgeinc/gcr-cleaner/pkg/gcrcleaner"
)

func main() {
	dry := flag.Bool("dry", false, "perform a dry run for testing")
	flag.Parse()

	jsonPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	jsonKey, err := ioutil.ReadFile(jsonPath)
	auther := gcrgoogle.NewJSONKeyAuthenticator(string(jsonKey))
	concurrency := runtime.NumCPU()

	cleaner, err := gcrcleaner.NewCleaner(auther, concurrency)
	if err != nil {
		log.Fatalf("failed to create cleaner: %s", err)
	}

	status, err := cleaner.Clean(*dry)
	if err != nil {
		log.Printf("failed to clean: %w", err)
	}

	if len(status) > 0 {
		if *dry {
			log.Printf("\nDRY RUN RESULTS:\n")
			
		} else {
			log.Printf("\nGCR CLEANER RESULTS:\n")
		}
		message := ""
		for _, s := range status {
			message += fmt.Sprintf("%s\n", s)
		}
		log.Printf(message)
	}
}
