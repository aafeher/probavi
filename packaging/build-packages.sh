#!/bin/sh
# Build every Linux package for one architecture.
#
# Used by the release workflow and by the CI job that installs the result,
# so what CI proves is exactly what a release publishes. Run from the
# repository root:
#
#   VERSION=0.3.0 ARCH=amd64 packaging/build-packages.sh dist/pkgroot dist
#
# Arguments: <staging dir holding the built binaries> <output dir>.
#
# Adapters come from the adapters/* glob, the same registry the release
# archives and the container image use, so a new adapter is packaged
# without editing anything here. internal/docs holds that set to
# docs/capabilities.json.

set -eu

BIN_DIR="${1:?staging directory with the built binaries}"
OUT_DIR="${2:?output directory}"
: "${VERSION:?VERSION is required}"
: "${ARCH:?ARCH is required (amd64 or arm64)}"

export BIN_DIR VERSION ARCH
mkdir -p "${OUT_DIR}"

render_and_pack() {
  template="$1"
  rendered="$(mktemp)"
  # envsubst rather than nfpm's own expansion: nfpm does not expand
  # variables in a content src or dst, and gets that wrong silently — the
  # package builds and installs with the binary at a literal ${VAR} path,
  # where the core's PATH lookup will never find it.
  envsubst < "${template}" > "${rendered}"
  for format in deb rpm apk; do
    nfpm package -f "${rendered}" -p "${format}" -t "${OUT_DIR}"
  done
  rm -f "${rendered}"
}

render_and_pack packaging/nfpm/probavi.yaml.tmpl

for dir in adapters/*/; do
  id="${dir#adapters/}"
  ADAPTER="${id%/}"
  # The engine's display name comes from the generated manifest, so a
  # package description cannot claim an engine the manifest does not
  # declare (AGENTS.md §5.8).
  ENGINE="$(jq -r --arg id "${ADAPTER}" '.adapters[] | select(.id == $id) | .name' docs/capabilities.json)"
  if [ -z "${ENGINE}" ] || [ "${ENGINE}" = "null" ]; then
    echo "packaging: adapters/${ADAPTER} is not declared in docs/capabilities.json" >&2
    exit 1
  fi
  export ADAPTER ENGINE
  render_and_pack packaging/nfpm/adapter.yaml.tmpl
done
