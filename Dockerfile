# Copyright 2019 The GCR Cleaner Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#####Builder Image#####
FROM golang:1.13 AS builder

RUN apt-get -qq update && apt-get -yqq install upx curl

ENV GO111MODULE=on \
  CGO_ENABLED=0 \
  GOOS=linux \
  GOARCH=amd64

WORKDIR /src

COPY . .
RUN go build \
  -a \
  -trimpath \
  -ldflags "-s -w -extldflags '-static'" \
  -installsuffix cgo \
  -tags netgo \
  -mod vendor \
  -o /bin/gcrcleaner \
  .

RUN strip /bin/gcrcleaner

RUN upx -q -9 /bin/gcrcleaner

RUN curl -LO https://storage.googleapis.com/kubernetes-release/release/`curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt`/bin/linux/amd64/kubectl; \
    chmod +x ./kubectl;


#####Final Image####
FROM debian:buster-slim

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/gcrcleaner /bin/gcrcleaner
COPY --from=builder /src/kubectl /usr/local/bin/kubectl

ENV PORT 8080

ENTRYPOINT ["/bin/gcrcleaner"]
