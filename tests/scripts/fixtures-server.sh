#!/usr/bin/env bash
# Lifecycle for the file_url fixture HTTP server (fixtures_server.py).
#
#   fixtures-server.sh start | stop | status | restart
#
# A systemd unit rather than a backgrounded process, for the reason CLAUDE.md
# already records: `bash -c "... &"` inside WSL does not keep a process alive
# past the invoking command returning, which is precisely how a "start" from a
# test script would be invoked.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_vars

UNIT=printgw-fixtures
ACTION="${1:-status}"

install_unit() {
  need_root
  cat > /etc/systemd/system/${UNIT}.service <<EOF
[Unit]
Description=file_url fixture HTTP server for LAB-16894 system tests
After=network.target

[Service]
Type=simple
Environment=FIXTURE_HOST=${FIXTURE_HOST}
Environment=FIXTURE_PORT=${FIXTURE_PORT}
ExecStart=/usr/bin/python3 ${SCRIPT_DIR}/fixtures_server.py
Restart=always
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

case "$ACTION" in
  start|restart)
    install_unit
    systemctl enable --now ${UNIT} >/dev/null 2>&1
    systemctl restart ${UNIT}
    deadline=$(( $(date +%s) + 15 ))
    while (( $(date +%s) < deadline )); do
      if curl -sf -o /dev/null --max-time 2 "http://${FIXTURE_HOST}:${FIXTURE_PORT}/health"; then
        say "fixtures up on ${FIXTURE_HOST}:${FIXTURE_PORT}"
        note "serving $TESTS_DIR/testdata plus /redirect /notfound /lying-chunked /slow"
        exit 0
      fi
      sleep 0.3
    done
    journalctl -u ${UNIT} -n 20 --no-pager >&2
    die "fixture server did not come up"
    ;;
  stop)
    need_root; systemctl stop ${UNIT} 2>/dev/null; say "fixtures stopped" ;;
  status)
    if curl -sf -o /dev/null --max-time 2 "http://${FIXTURE_HOST}:${FIXTURE_PORT}/health"; then
      say "fixtures up on ${FIXTURE_HOST}:${FIXTURE_PORT}"
    else
      warn "fixtures NOT running (${FIXTURE_HOST}:${FIXTURE_PORT})"; exit 1
    fi
    ;;
  *) echo "usage: fixtures-server.sh start|stop|status|restart"; exit 1 ;;
esac
