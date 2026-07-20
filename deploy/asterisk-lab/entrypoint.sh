#!/bin/sh
set -eu

: "${PBX_BIND_IP:?PBX_BIND_IP is required}"
: "${PBX_SIP_PORT:?PBX_SIP_PORT is required}"
: "${PBX_LOCAL_NET:?PBX_LOCAL_NET is required}"
: "${GATEWAY_SIP_IP:?GATEWAY_SIP_IP is required}"
: "${GATEWAY_SIP_PORT:?GATEWAY_SIP_PORT is required}"
: "${EXTENSION_6001_PASSWORD:?EXTENSION_6001_PASSWORD is required}"
: "${EXTENSION_6002_PASSWORD:?EXTENSION_6002_PASSWORD is required}"

umask 0077
envsubst \
    '${PBX_BIND_IP} ${PBX_SIP_PORT} ${PBX_LOCAL_NET} ${GATEWAY_SIP_IP} ${GATEWAY_SIP_PORT} ${EXTENSION_6001_PASSWORD} ${EXTENSION_6002_PASSWORD}' \
    < /opt/asterisk-lab/templates/pjsip.conf.tmpl \
    > /etc/asterisk/pjsip.conf

cp /opt/asterisk-lab/templates/extensions.conf /etc/asterisk/extensions.conf
cp /opt/asterisk-lab/templates/rtp.conf /etc/asterisk/rtp.conf
cp /opt/asterisk-lab/templates/logger.conf /etc/asterisk/logger.conf
cp /opt/asterisk-lab/templates/modules.conf /etc/asterisk/modules.conf

chown root:asterisk \
    /etc/asterisk/pjsip.conf \
    /etc/asterisk/extensions.conf \
    /etc/asterisk/rtp.conf \
    /etc/asterisk/logger.conf \
    /etc/asterisk/modules.conf
chmod 0640 \
    /etc/asterisk/pjsip.conf \
    /etc/asterisk/extensions.conf \
    /etc/asterisk/rtp.conf \
    /etc/asterisk/logger.conf \
    /etc/asterisk/modules.conf

exec /usr/sbin/asterisk -f -U asterisk -G asterisk -vvv
