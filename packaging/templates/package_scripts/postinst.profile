#!/bin/bash
# This is a postinstallation script so the service can be configured and started when requested
#
if [ -d "/var/lib/bor" ]
then
    echo "Directory /var/lib/bor exists."
else
    mkdir -p /var/lib/bor
    sudo chown -R bor /var/lib/bor
fi
sudo systemctl daemon-reload
