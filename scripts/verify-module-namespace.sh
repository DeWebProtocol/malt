#!/bin/sh
set -eu

expected_module='github.com/dewebprotocol/malt-client'
script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_directory/.." && pwd)

release_tag=''
if [ "$#" -ne 0 ]; then
	if [ "$#" -ne 2 ] || [ "$1" != '--release-tag' ] || [ -z "$2" ]; then
		echo 'usage: verify-module-namespace.sh [--release-tag TAG]' >&2
		exit 2
	fi
	release_tag=$2
fi

actual_module=$(cd "$repository_root" && go list -m -f '{{.Path}}')
if [ "$actual_module" != "$expected_module" ]; then
	echo "runtime module namespace changed without the dedicated cutover: got $actual_module, want $expected_module" >&2
	exit 1
fi

case "${GITHUB_REF_TYPE:-}:${GITHUB_REF:-}" in
	tag:*|*:refs/tags/*)
		release_tag=${GITHUB_REF_NAME:-${GITHUB_REF#refs/tags/}}
		;;
esac

if [ -n "$release_tag" ]; then
	echo "runtime release tag $release_tag is blocked while the module namespace decision is deferred" >&2
	echo 'complete docs/runtime-module-namespace.md in a dedicated cutover PR before tagging' >&2
	exit 1
fi

echo "runtime module namespace gate: $actual_module (release tags blocked)"
