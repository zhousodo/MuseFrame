#!/bin/bash
set -e
PASS=$(awk '{print $3}' ~/gitea-login.txt)
rm -rf /tmp/mfrepo
git clone -b main ~/museframe.bundle /tmp/mfrepo
cd /tmp/mfrepo
git remote add gitea "http://zhousodo:${PASS}@127.0.0.1:3000/zhousodo/museframe.git"
git push gitea main && echo MUSEFRAME_PUSHED
cd /; rm -rf /tmp/mfrepo ~/museframe.bundle
