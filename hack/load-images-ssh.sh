#!/usr/bin/env bash

# Copyright 2023 Antrea Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script is used to load Antrea UI docker images to the K8s Nodes created
# as part of the Antrea Vagrant testbed.
# Refer to https://github.com/antrea-io/antrea/tree/main/test/e2e

set -eo pipefail

SSH_CONFIG="$1"

# Read all hosts in ssh-config file
HOST_PATTERN="Host (k8s-node-.*)"
ALL_HOSTS=()
while IFS= read -r line; do
    if [[ $line =~ $HOST_PATTERN ]]; then
        ALL_HOSTS+=( "${BASH_REMATCH[1]}" )
    fi
done < $SSH_CONFIG

function waitForNodes {
    pids=("$@")
    for pid in "${pids[@]}"; do
        if ! wait $pid; then
            echo "Command failed for one or more node"
            wait # wait for all remaining processes
            exit 2
        fi
    done
}

function pushImgToNodes() {
    IMG_NAME=$1
    SAVED_IMG=$2

    docker inspect $IMG_NAME > /dev/null
    if [ $? -ne 0 ]; then
        echo "Docker image $IMG_NAME was not found"
        exit 1
    fi

    echo "Saving $IMG_NAME image to $SAVED_IMG"
    docker save -o $SAVED_IMG $IMG_NAME

    echo "Copying $IMG_NAME image to every node..."
    pids=()
    for name in "${ALL_HOSTS[@]}"; do
        scp -F $SSH_CONFIG $SAVED_IMG $name:/tmp/image.tar &
        pids+=($!)
    done
    # Wait for all child processes to complete
    waitForNodes "${pids[@]}"
    echo "Done!"

    echo "Loading $IMG_NAME image in every node..."
    pids=()
    for name in "${ALL_HOSTS[@]}"; do
        ssh -F $SSH_CONFIG $name "ctr -n=k8s.io image import /tmp/image.tar; rm -f /tmp/image.tar" &
        pids+=($!)
    done
    # Wait for all child processes to complete
    waitForNodes "${pids[@]}"
    rm -f $SAVED_IMG
    echo "Done!"
}

pushImgToNodes antrea/antrea-ui-frontend:latest /tmp/antrea-ui-frontend.tar
pushImgToNodes antrea/antrea-ui-backend:latest /tmp/antrea-ui-backend.tar
