#!/bin/bash

# needs gettext to use envsubst
# should already be installed -- no root in postStart hook

mkdir -p $HOME/.continue; (command -v envsubst && envsubst < /opt/share/continue/config.json > $HOME/.continue/config.json) &>/dev/null


