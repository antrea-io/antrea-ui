#!/usr/bin/env bash
# Copyright 2023 Antrea Authors.
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


ME=$(basename $0)

if [ "$NGINX_RELOADER_DIRECTORIES" == "" ]; then
    echo "$ME: no directories to watch, exiting"
    exit 0
fi

data=$(find $NGINX_RELOADER_DIRECTORIES -type f -exec sha256sum {} \; | sort)

# monitor checks for any changes in the provided list of directories, every 60s. inotify-tools is
# not available in the image (and it is not convenient to install it as the image uses a non-root
# user), so we use a simple loop. In practice, this is used to reload new SSL certificates when they
# are rotated, so a 60s delay is fine.
function monitor {
    while true; do
        sleep 60
        new_data=$(find $NGINX_RELOADER_DIRECTORIES -type f -exec sha256sum {} \; | sort)
        if [ "$new_data" != "$data" ]; then
            echo "$ME: detected change; executing: nginx -s reload"
            nginx -s reload
            data="$new_data"
        fi
    done
}

monitor &
