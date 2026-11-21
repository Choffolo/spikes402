#!/bin/sh
# Generate CycloneDX SBOMs and scan them. Fail only on reachable or
# unaddressed findings (see vex/spikes402.openvex.json).
#
#   ./scripts/sbom.sh           # generate + scan (default)
#   ./scripts/sbom.sh generate  # SBOMs only
#   ./scripts/sbom.sh scan      # scan existing SBOMs / the tree
#
# Tools (pinned, checksum-verified on linux/amd64):
#   syft 1.57.2, grype 0.122.1, osv-scanner 2.7.1, govulncheck v1.10.0
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

SYFT_VERSION=1.57.2
GRYPE_VERSION=0.122.1
OSV_VERSION=2.7.1
GOVULNCHECK_VERSION=v1.10.0
SYFT_SHA256_LINUX_AMD64=ad92bd3ec1b94a3df01d4d0aec199afa9c42b72e86da436ff5f837914d32706b
GRYPE_SHA256_LINUX_AMD64=f1ba7e2917ee1395618de20a98da0aa7c0fb1788927797651047c50502a37538
OSV_SHA256_LINUX_AMD64=fb7dab19a8bd9bbe5c3189f09d4b7ee1820c9553a23bac202648a56efd2f6ee8

TOOLDIR=${SPIKES402_TOOLDIR:-${XDG_CACHE_HOME:-$HOME/.cache}/spikes402-sbom-tools}
SBOM_DIR="$ROOT/sbom"
VEX="$ROOT/vex/spikes402.openvex.json"
IMAGE_TAG=spikes402:sbom
SOURCE_BOM="$SBOM_DIR/spikes402-source.cdx.json"
IMAGE_BOM="$SBOM_DIR/spikes402-image.cdx.json"
# Syft records these file locations as components if file catalogers stay on.
SYFT_CATALOGERS="-file"

action=${1:-all}

die() { echo "sbom.sh: $*" >&2; exit 1; }

# Run docker as the current user, or with passwordless sudo (local box).
docker_ok() {
	if docker info >/dev/null 2>&1; then
		return 0
	fi
	if sudo -n docker info >/dev/null 2>&1; then
		return 0
	fi
	return 1
}

docker_cmd() {
	if docker info >/dev/null 2>&1; then
		docker "$@"
	else
		sudo -n docker "$@"
	fi
}

need_linux_amd64() {
	os=$(uname -s)
	arch=$(uname -m)
	if [ "$os" != "Linux" ] || [ "$arch" != "x86_64" ]; then
		die "pinned tool checksums are for linux/amd64 (this host is $os/$arch); run this in CI or install syft, grype and osv-scanner yourself and set SPIKES402_TOOLDIR"
	fi
}

download_verified() {
	# download_verified <url> <sha256> <dest>
	url=$1
	want=$2
	dest=$3
	if [ -f "$dest" ]; then
		got=$(sha256sum "$dest" | awk '{print $1}')
		if [ "$got" = "$want" ]; then
			return 0
		fi
	fi
	tmp=$dest.tmp
	curl -sSfL -o "$tmp" "$url" || die "download failed: $url"
	got=$(sha256sum "$tmp" | awk '{print $1}')
	[ "$got" = "$want" ] || die "checksum mismatch for $url (got $got, want $want)"
	mv "$tmp" "$dest"
}

install_tools() {
	need_linux_amd64
	mkdir -p "$TOOLDIR"
	if [ ! -x "$TOOLDIR/syft" ]; then
		download_verified \
			"https://github.com/anchore/syft/releases/download/v${SYFT_VERSION}/syft_${SYFT_VERSION}_linux_amd64.tar.gz" \
			"$SYFT_SHA256_LINUX_AMD64" \
			"$TOOLDIR/syft.tgz"
		tar -xzf "$TOOLDIR/syft.tgz" -C "$TOOLDIR" syft
		chmod +x "$TOOLDIR/syft"
	fi
	if [ ! -x "$TOOLDIR/grype" ]; then
		download_verified \
			"https://github.com/anchore/grype/releases/download/v${GRYPE_VERSION}/grype_${GRYPE_VERSION}_linux_amd64.tar.gz" \
			"$GRYPE_SHA256_LINUX_AMD64" \
			"$TOOLDIR/grype.tgz"
		tar -xzf "$TOOLDIR/grype.tgz" -C "$TOOLDIR" grype
		chmod +x "$TOOLDIR/grype"
	fi
	if [ ! -x "$TOOLDIR/osv-scanner" ]; then
		download_verified \
			"https://github.com/google/osv-scanner/releases/download/v${OSV_VERSION}/osv-scanner_linux_amd64" \
			"$OSV_SHA256_LINUX_AMD64" \
			"$TOOLDIR/osv-scanner"
		chmod +x "$TOOLDIR/osv-scanner"
	fi
	PATH="$TOOLDIR:$PATH"
	export PATH
}

source_version() {
	if command -v git >/dev/null 2>&1 && [ -e "$ROOT/.git" ]; then
		git -C "$ROOT" rev-parse --short=12 HEAD
	else
		echo unknown
	fi
}

generate() {
	install_tools
	mkdir -p "$SBOM_DIR"
	ver=$(source_version)
	echo "Generating source SBOM (Go module, extension npm, GitHub Actions) at $ver..."
	syft scan dir:"$ROOT" \
		--source-name spikes402 \
		--source-version "$ver" \
		--select-catalogers "$SYFT_CATALOGERS" \
		-o "cyclonedx-json=$SOURCE_BOM"

	if docker_ok; then
		echo "Building $IMAGE_TAG for the image SBOM..."
		docker_cmd build -t "$IMAGE_TAG" "$ROOT"
		archive=$SBOM_DIR/spikes402-image.tar
		# Write the archive as this user even if docker itself ran via sudo.
		docker_cmd save "$IMAGE_TAG" > "$archive"
		echo "Generating image SBOM..."
		syft scan docker-archive:"$archive" \
			--source-name spikes402 \
			--source-version "$ver" \
			--select-catalogers "$SYFT_CATALOGERS" \
			-o "cyclonedx-json=$IMAGE_BOM"
		rm -f "$archive"
	else
		echo "Docker is not available; skipping the image SBOM."
		rm -f "$IMAGE_BOM"
	fi

	python3 - "$SOURCE_BOM" "$IMAGE_BOM" <<'PY'
import json, sys
from collections import Counter
from pathlib import Path

def summarize(path):
    if not path or not Path(path).is_file():
        return
    doc = json.loads(Path(path).read_text())
    comps = doc.get("components") or []
    kinds = Counter((c.get("type") or "?", ((c.get("purl") or "no-purl").split("/")[0])) for c in comps)
    print(f"  {Path(path).name}: {len(comps)} components")
    for (typ, eco), n in kinds.most_common():
        print(f"    {n:4d}  {typ}  {eco}")

print("SBOM summary:")
summarize(sys.argv[1])
summarize(sys.argv[2])
PY
}

# Grype after VEX: any leftover match of severity >= negligible fails.
grype_sbom() {
	bom=$1
	label=$2
	[ -f "$bom" ] || return 0
	echo "Grype ($label)..."
	out=$SBOM_DIR/grype-$(basename "$bom" .cdx.json).json
	# Exit 2 means "found a vuln at the cutoff"; we inspect the report ourselves
	# so VEX-filtered results can be clean.
	set +e
	grype "sbom:$bom" --vex "$VEX" -o json --file "$out" --fail-on negligible
	code=$?
	set -e
	python3 - "$out" "$label" <<'PY'
import json, sys
from pathlib import Path
doc = json.loads(Path(sys.argv[1]).read_text())
matches = doc.get("matches") or []
if matches:
    print(f"Grype ({sys.argv[2]}): {len(matches)} unaddressed finding(s):")
    for m in matches:
        v = m.get("vulnerability") or {}
        a = m.get("artifact") or {}
        print(f"  {v.get('id')}  {v.get('severity')}  {a.get('name')}@{a.get('version')}")
    sys.exit(1)
print(f"Grype ({sys.argv[2]}): no unaddressed findings.")
PY
	# 0 = clean, 2 = vulns (already handled), anything else is a tool error
	if [ "$code" -ne 0 ] && [ "$code" -ne 2 ]; then
		die "grype failed with exit $code"
	fi
}

scan() {
	install_tools
	[ -f "$VEX" ] || die "missing $VEX"
	[ -f "$SOURCE_BOM" ] || die "missing $SOURCE_BOM; run: $0 generate"

	echo "govulncheck (reachable Go vulnerabilities, $GOVULNCHECK_VERSION)..."
	if ! command -v go >/dev/null 2>&1; then
		die "go is required for govulncheck"
	fi
	go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./...

	echo "OSV-Scanner on the source tree (go.mod, package-lock.json, Go call analysis)..."
	osv_src=$SBOM_DIR/osv-source.json
	set +e
	osv-scanner scan source -r --x-exclude sbom --call-analysis=go --format json --output-file "$osv_src" "$ROOT"
	osv_code=$?
	set -e
	if [ "$osv_code" -gt 1 ]; then
		die "osv-scanner source scan failed with exit $osv_code"
	fi
	# Exit 0 = no vulns, 1 = vulns found; either way, apply VEX.
	if [ ! -s "$osv_src" ]; then
		echo '{"results":[]}' > "$osv_src"
	fi
	python3 "$ROOT/scripts/vex-filter.py" "$osv_src" "$VEX"

	grype_sbom "$SOURCE_BOM" "source SBOM"
	grype_sbom "$IMAGE_BOM" "image SBOM"

	if docker_ok && docker_cmd image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
		echo "OSV-Scanner on the Docker image..."
		archive=$SBOM_DIR/spikes402-image.tar
		docker_cmd save "$IMAGE_TAG" > "$archive"
		osv_img=$SBOM_DIR/osv-image.json
		set +e
		osv-scanner scan image --archive "$archive" --format json --output-file "$osv_img"
		osv_code=$?
		set -e
		rm -f "$archive"
		if [ "$osv_code" -gt 1 ]; then
			die "osv-scanner image scan failed with exit $osv_code"
		fi
		if [ ! -s "$osv_img" ]; then
			echo '{"results":[]}' > "$osv_img"
		fi
		python3 "$ROOT/scripts/vex-filter.py" "$osv_img" "$VEX"
	else
		echo "Docker image $IMAGE_TAG is not available; skipping the image OSV scan."
	fi

	echo "Scan finished: no reachable or unaddressed issues."
}

case $action in
	generate) generate ;;
	scan) scan ;;
	all) generate; scan ;;
	*) die "unknown action '$action' (use generate, scan, or all)" ;;
esac
