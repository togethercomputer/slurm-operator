#!/usr/bin/env bash

set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../helm/slurm" && pwd)"

rendered="$(
    helm template test "$chart_dir" \
        --namespace slurm \
        --set controller.enabled=false \
        --set controller.external=true \
        --set controller.externalConfig.host=controller.example \
        --set controller.externalConfig.port=6817 \
        --set login.enabled=true \
        --set slurmKeyRef.name=external-auth \
        --set slurmKeyRef.key=custom.key \
        --show-only templates/login/login-deployment.yaml
)"

for expected in 'key: "custom.key"' 'path: slurm.key'; do
    if [[ "$rendered" != *"$expected"* ]]; then
        printf 'external login render is missing %q\n' "$expected" >&2
        exit 1
    fi
done

if helm template test "$chart_dir" \
    --namespace slurm \
    --set controller.enabled=false \
    --set controller.external=true \
    --set controller.externalConfig.host=controller.example \
    --set controller.externalConfig.port=6817 \
    --set login.enabled=true \
    --set slurmKeyRef.name=external-auth \
    --show-only templates/login/login-deployment.yaml >/dev/null 2>&1; then
    printf 'external login render accepted an empty slurmKeyRef.key\n' >&2
    exit 1
fi
