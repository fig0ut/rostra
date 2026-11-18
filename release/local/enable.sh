#!/usr/bin/env bash

set -e -o pipefail

sudo systemctl enable rostra
sudo systemctl start rostra
sudo journalctl -u rostra --output cat -f
