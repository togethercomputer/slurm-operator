# syntax=docker/dockerfile:1
# Wrapper over the upstream login image: upgrade ca-certificates from
# noble-security (CVE-2026-111943) until upstream ships a fixed tag.
FROM ghcr.io/slinkyproject/login:25.11-ubuntu24.04
USER root
RUN apt-get update \
  && apt-get install -y --only-upgrade ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && v="$(dpkg-query -W -f='${Version}' ca-certificates)" \
  && echo "ca-certificates ${v}" \
  && dpkg --compare-versions "${v}" ge '20260816~'
