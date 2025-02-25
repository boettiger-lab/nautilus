#!/bin/bash

# needs gettext to use envsubst


if [ ! -f "$HOME/.continue/config.json" ];
then 
	mkdir -p $HOME/.continue
  (command -v envsubst && envsubst < /opt/share/continue/config.json > $HOME/.continue/config.json) &>/dev/null
	chown -R ${NB_USER} .continue/config.json
fi


