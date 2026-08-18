#!/bin/sh
# The ngx installer for Unix (Linux and macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sh
#
# Written in POSIX sh on purpose: whoever runs "curl | sh" may not have bash,
# and the installer has to work before any dependency exists.
#
# The order of the steps is deliberate: everything that can fail without the
# network fails BEFORE the first download — platform, installation directory,
# write permission and verification tools. Spending the download only to then
# find out that permission is missing is a waste, and worse: it leaves junk on
# disk.

set -eu

REPOSITORY="s0beran0/ngx"
API_URL="https://api.github.com/repos/${REPOSITORY}"
RELEASES_URL="https://github.com/${REPOSITORY}/releases"

# ---------------------------------------------------------------------------
# PLACEHOLDER: MINISIGN PUBLIC KEY (DD2/DD3)
# ---------------------------------------------------------------------------
# The project's public key HAS NOT BEEN GENERATED YET (Task D2). The value
# below is a deliberate placeholder and is NOT a key: a real minisign key is a
# single base64 line of 56 characters starting with "RW". The text was written
# to be impossible to mistake for a real key — a plausible value would slip
# through review and reach production verifying nothing.
#
# When generating the key (the same one that goes into ngx-minisign.pub and
# into the repository's NGX_PUBLIC_KEY variable), replace the line below with
# the key line from the .pub file — the second line, without the "untrusted
# comment:" part.
#
# While the placeholder is here, the script REFUSES to install: absence of
# verification is a failure, never a "carried on anyway".
MINISIGN_PUBLIC_KEY="RWSZFXRcIf6p0xLvenNPLgltwYLa/qRAjNH3sA238fWZIy49RGIbtgAW"
KEY_PLACEHOLDER="PLACEHOLDER-CHAVE-MINISIGN-NAO-GERADA-VER-TASK-D2"

# Configurable through the environment. No surprising default values.
# The default uses "-" and not ":-": NGX_INSTALL_DIR set but empty almost
# always comes from a variable that did not expand the way the person
# expected, and falling back to /usr/local/bin in that case would install
# somewhere other than what was asked for.
NGX_INSTALL_DIR="${NGX_INSTALL_DIR-/usr/local/bin}"
NGX_CHANNEL="${NGX_CHANNEL:-stable}"
NGX_VERSION="${NGX_VERSION:-}"
NGX_ALLOW_UNVERIFIED="${NGX_ALLOW_UNVERIFIED:-0}"

TEMP_DIR=""
PARTIAL_FILE=""
HTTP_TOOL=""
SHA256_TOOL=""
SIGNATURE_CHECKED=0

# ---------------------------------------------------------------------------
# Utilities
# ---------------------------------------------------------------------------

error() {
    printf 'error: %s\n' "$1" >&2
}

line() {
    printf '%s\n' "$1" >&2
}

inform() {
    printf '%s\n' "$1" >&2
}

cleanup() {
    if [ -n "$PARTIAL_FILE" ] && [ -e "$PARTIAL_FILE" ]; then
        rm -f "$PARTIAL_FILE"
    fi
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        rm -rf "$TEMP_DIR"
    fi
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM
trap 'cleanup; exit 129' HUP

show_help() {
    cat <<'END'
install.sh — the ngx installer for Linux and macOS

USAGE
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sh
  sh install.sh [--help]

ENVIRONMENT VARIABLES
  NGX_INSTALL_DIR      Installation directory. Default: /usr/local/bin
                       A system directory requires privilege; the script never
                       calls sudo on its own, it only prints the exact line to
                       run.
  NGX_CHANNEL          stable (default) or beta. beta includes pre-releases
                       (-rc, -beta, -alpha).
  NGX_VERSION          Pinned version, e.g. v0.2.0. When set, the GitHub API
                       is not queried.
  NGX_ALLOW_UNVERIFIED If 1, allows installing when the minisign signature
                       CANNOT be verified (minisign missing or public key not
                       generated yet). The warning is printed prominently. It
                       does NOT ignore an invalid signature or a mismatched
                       checksum: those always abort, no exceptions.

EXAMPLES
  # system-wide installation, with privilege
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sudo sh

  # unprivileged installation, in the user's directory
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh \
    | NGX_INSTALL_DIR=$HOME/.local/bin sh

  # pinned version
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh \
    | NGX_VERSION=v0.2.0 sh

VERIFICATION
  The SHA256 checksum is always checked and there is no way to turn it off.
  The minisign signature of checksums.txt is checked when minisign is
  installed and the project's public key is embedded in this script.
END
}

have() {
    command -v "$1" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Step 1 — arguments
# ---------------------------------------------------------------------------

for argument in "$@"; do
    case "$argument" in
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            error "unknown argument: $argument"
            line ""
            line "run 'sh install.sh --help' to see the options."
            exit 2
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Step 2 — platform
# ---------------------------------------------------------------------------

detect_platform() {
    raw_system="$(uname -s)"
    raw_arch="$(uname -m)"

    case "$raw_system" in
        Linux)  SYSTEM="linux" ;;
        Darwin) SYSTEM="darwin" ;;
        MINGW*|MSYS*|CYGWIN*|Windows_NT)
            error "this script is for Linux and macOS; the detected system was $raw_system"
            line ""
            line "on Windows use the PowerShell installer:"
            line "  irm https://raw.githubusercontent.com/${REPOSITORY}/main/install.ps1 | iex"
            exit 1
            ;;
        *)
            error "unsupported operating system: $raw_system"
            line ""
            line "ngx publishes binaries for linux and darwin (macOS)."
            line "for other platforms, build from source:"
            line "  git clone https://github.com/${REPOSITORY}.git && cd ngx && make build"
            exit 1
            ;;
    esac

    case "$raw_arch" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        *)
            error "unsupported architecture: ${raw_system}/${raw_arch}"
            line ""
            line "ngx publishes binaries for amd64 (x86_64) and arm64 (aarch64)."
            line "for ${raw_arch}, build from source:"
            line "  git clone https://github.com/${REPOSITORY}.git && cd ngx && make build"
            exit 1
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Step 3 — installation directory and permission
# ---------------------------------------------------------------------------
#
# The script does NOT call sudo. Escalating privilege on its own inside
# something executed through "curl | sh" is exactly what nobody should agree
# to run: the person decides to elevate, with the command in front of them.

privilege_message() {
    reason="$1"
    error "$reason"
    line ""
    line "run the installation with privilege:"
    line "  curl -fsSL https://raw.githubusercontent.com/${REPOSITORY}/main/install.sh | sudo sh"
    line ""
    line "or install into a directory of your own, without privilege:"
    line "  curl -fsSL https://raw.githubusercontent.com/${REPOSITORY}/main/install.sh | NGX_INSTALL_DIR=\$HOME/.local/bin sh"
    line ""
    line "if you pick the second, make sure \$HOME/.local/bin is in the PATH."
    exit 1
}

prepare_directory() {
    if [ -z "$NGX_INSTALL_DIR" ]; then
        error "NGX_INSTALL_DIR is set and empty"
        line ""
        line "leave the variable unset to use /usr/local/bin, or point it at a"
        line "directory: NGX_INSTALL_DIR=\$HOME/.local/bin"
        exit 2
    fi

    if [ -e "$NGX_INSTALL_DIR" ] && [ ! -d "$NGX_INSTALL_DIR" ]; then
        error "$NGX_INSTALL_DIR exists and is not a directory"
        exit 1
    fi

    if [ ! -d "$NGX_INSTALL_DIR" ]; then
        if ! mkdir -p "$NGX_INSTALL_DIR" 2>/dev/null; then
            privilege_message "could not create the directory $NGX_INSTALL_DIR"
        fi
    fi

    # Actually writing is the only test that does not lie: [ -w ] gets it
    # wrong on a read-only mounted filesystem, on ACLs and in a container with
    # a mapped user.
    test_file="${NGX_INSTALL_DIR}/.ngx-write-test.$$"
    if ! (umask 077 && : > "$test_file") 2>/dev/null; then
        privilege_message "no write permission in $NGX_INSTALL_DIR"
    fi
    rm -f "$test_file"
}

# ---------------------------------------------------------------------------
# Step 4 — tools and verification (before downloading)
# ---------------------------------------------------------------------------

choose_http_tool() {
    if have curl; then
        HTTP_TOOL="curl"
    elif have wget; then
        HTTP_TOOL="wget"
    else
        error "neither curl nor wget was found"
        line ""
        line "install one of them and run again. on Debian/Ubuntu:"
        line "  apt-get install -y curl"
        exit 1
    fi
}

choose_sha256_tool() {
    if have sha256sum; then
        SHA256_TOOL="sha256sum"
    elif have shasum; then
        SHA256_TOOL="shasum"
    else
        error "no SHA256 tool found (sha256sum or shasum)"
        line ""
        line "the checksum is mandatory and there is no way to turn it off:"
        line "installing a binary without checking the hash would accept any"
        line "download that came corrupted or was swapped along the way."
        line ""
        line "on Debian/Ubuntu: apt-get install -y coreutils"
        line "on Alpine:        apk add coreutils"
        exit 1
    fi
}

# Decides, BEFORE downloading, whether the signature can be verified. There
# are three outcomes, and none of them is silent:
#   - it can be verified       -> carry on, and the signature will be checked
#   - it cannot, unauthorized  -> abort here, saying why and how to fix it
#   - it cannot, authorized    -> carry on with a prominent warning
# openssl_can_verify_ed25519 says whether this openssl can do the two
# computations a minisign signature requires. It is not enough for openssl to
# exist: the LibreSSL Apple ships, for instance, has no BLAKE2b. Testing the
# capability is more reliable than reading a version number.
openssl_can_verify_ed25519() {
    have openssl || return 1
    printf x | openssl dgst -blake2b512 >/dev/null 2>&1 || return 1
    openssl list -public-key-algorithms 2>/dev/null | grep -qi ed25519 || return 1
    return 0
}

# verify_signature_with_openssl reimplements minisign's verification.
#
# The format, for whoever comes to touch this: the public key is base64 of 2
# algorithm bytes + 8 of key id + 32 of the Ed25519 key; the signature, on the
# second line of the .minisig, is base64 of 2 + 8 + 64 signature bytes. The
# "ED" algorithm means pre-hashed: what is signed is the file's BLAKE2b-512,
# not the file.
#
# The DER prefix embedded below turns the 32 raw bytes into a public key
# openssl accepts. It is fixed for Ed25519.
verify_signature_with_openssl() {
    signature_path="$1"
    tmp_verify="$(mktemp -d)" || { error "could not create temporary directory"; exit 1; }

    printf %s "$MINISIGN_PUBLIC_KEY" | openssl base64 -d -A 2>/dev/null \
        | tail -c 32 > "${tmp_verify}/pub.raw"
    # 302a300506032b6570032100, in octal so as not to depend on xxd.
    printf '\060\052\060\005\006\003\053\145\160\003\041\000' > "${tmp_verify}/pub.der"
    cat "${tmp_verify}/pub.raw" >> "${tmp_verify}/pub.der"

    sed -n 2p "$signature_path" | openssl base64 -d -A 2>/dev/null \
        | tail -c 64 > "${tmp_verify}/sig.bin"

    openssl dgst -blake2b512 -binary "$CHECKSUMS_PATH" > "${tmp_verify}/digest.bin" 2>/dev/null

    if ! openssl pkeyutl -verify -pubin -inkey "${tmp_verify}/pub.der" -keyform DER \
        -rawin -in "${tmp_verify}/digest.bin" -sigfile "${tmp_verify}/sig.bin" >/dev/null 2>&1; then
        rm -rf "$tmp_verify"
        error "the signature of checksums.txt does NOT check out (verified with openssl)"
        line ""
        line "the downloaded file was not signed by the project's key. this is"
        line "not a network error: it is an artifact that should not exist."
        line ""
        line "nothing was installed. do not work around this error."
        exit 1
    fi

    rm -rf "$tmp_verify"
    inform "signature checked (via openssl; minisign is not installed)."
}

assess_signature_verification() {
    reason=""

    if [ "$MINISIGN_PUBLIC_KEY" = "$KEY_PLACEHOLDER" ]; then
        reason="the project's minisign public key has not been generated yet and this script carries a placeholder"
    elif have minisign; then
        VERIFIER="minisign"
    elif openssl_can_verify_ed25519; then
        # Most servers do not have minisign, and requiring them to install a
        # package just to install ngx is friction that pushes everyone toward
        # NGX_ALLOW_UNVERIFIED -- that is, the security requirement would end
        # up producing less verification, not more.
        #
        # A minisign signature is Ed25519 over BLAKE2b-512 (pre-hashed "ED"
        # mode), and openssl 3 does both. Verified on a real Oracle Linux 9,
        # which has no minisign and does have openssl.
        VERIFIER="openssl"
    else
        reason="neither minisign nor an openssl with Ed25519 and BLAKE2b is available"
    fi

    if [ -z "$reason" ]; then
        SIGNATURE_CHECKED=1
        return 0
    fi

    if [ "$NGX_ALLOW_UNVERIFIED" = "1" ]; then
        SIGNATURE_CHECKED=0
        line ""
        line "############################################################"
        line "# WARNING: INSTALLING WITHOUT VERIFYING THE SIGNATURE"
        line "#"
        line "# $reason."
        line "#"
        line "# NGX_ALLOW_UNVERIFIED=1 is set, so the installation carries"
        line "# on. The SHA256 checksum will still be checked, but it only"
        line "# protects against a corrupted download: it does not protect"
        line "# against a release published by whoever has compromised the"
        line "# GitHub account, because in that case the checksum would come"
        line "# tampered with as well."
        line "############################################################"
        line ""
        return 0
    fi

    error "the release signature could not be verified"
    line ""
    line "reason: $reason."
    line ""
    line "ngx runs as root on servers that serve traffic. installing a binary"
    line "without verifying where it came from is not a hygiene detail."
    line "that is why the script stops here instead of carrying on."
    line ""
    line "how to fix it:"
    if [ "$MINISIGN_PUBLIC_KEY" = "$KEY_PLACEHOLDER" ]; then
        line "  the public key does not exist yet — there is nothing you can do"
        line "  on your side. follow ${RELEASES_URL} and use a version"
        line "  of this script published after the first signed release."
    else
        line "  install minisign OR an openssl 3 and run again:"
        line "    Debian/Ubuntu:   apt-get install -y minisign"
        line "    Alpine:          apk add minisign"
        line "    RHEL/Oracle/Fed: dnf install -y openssl"
        line "    macOS:           brew install minisign"
    fi
    line ""
    line "if you accept the risk knowingly, and only in that case:"
    line "  NGX_ALLOW_UNVERIFIED=1 sh install.sh"
    exit 1
}

# ---------------------------------------------------------------------------
# Step 5 — version resolution
# ---------------------------------------------------------------------------

# Downloads a URL into a file and prints the HTTP code on stdout.
download_to() {
    url="$1"
    target="$2"

    if [ "$HTTP_TOOL" = "curl" ]; then
        curl --proto '=https' --tlsv1.2 -sSL \
            --connect-timeout 15 --retry 2 --retry-delay 1 \
            -o "$target" -w '%{http_code}' "$url" 2>/dev/null || printf '000'
    else
        if wget -q --timeout=15 --tries=3 -O "$target" "$url" 2>/dev/null; then
            printf '200'
        else
            printf '000'
        fi
    fi
}

release_failure() {
    code="$1"
    where="$2"

    case "$code" in
        404)
            error "no release found for ${REPOSITORY} (${where} answered 404)"
            line ""
            line "the two possible causes:"
            line "  1. the project has not published any release yet. check at"
            line "     ${RELEASES_URL}"
            if [ -n "$NGX_VERSION" ]; then
                line "  2. the requested version, ${NGX_VERSION}, does not exist. the"
                line "     tag name includes the leading 'v': NGX_VERSION=v0.1.0, not 0.1.0."
            else
                line "  2. only pre-releases exist. try the beta channel:"
                line "     NGX_CHANNEL=beta sh install.sh"
            fi
            ;;
        403|429)
            error "the GitHub API refused the query (HTTP ${code}) — likely a per-IP rate limit"
            line ""
            line "the anonymous limit is per hour and per address. two ways out:"
            line "  - wait and try again, or"
            line "  - pin the version, which skips the API query:"
            line "      NGX_VERSION=v0.1.0 sh install.sh"
            ;;
        000)
            error "could not talk to ${where}"
            line ""
            line "check the network connection, DNS and whether a proxy requires"
            line "configuration (https_proxy). no file was written."
            ;;
        *)
            error "unexpected response from ${where}: HTTP ${code}"
            line ""
            line "check the service status at https://www.githubstatus.com"
            ;;
    esac
    exit 1
}

# Extracts the first "tag_name" from a release JSON. No jq: the installer
# cannot depend on a tool the machine may not have.
first_tag() {
    tr ',' '\n' < "$1" \
        | grep -m 1 '"tag_name"' \
        | sed -e 's/.*"tag_name"[[:space:]]*:[[:space:]]*"//' -e 's/".*//'
}

resolve_version() {
    if [ -n "$NGX_VERSION" ]; then
        VERSION="$NGX_VERSION"
        return 0
    fi

    case "$NGX_CHANNEL" in
        stable) query_url="${API_URL}/releases/latest" ;;
        beta)   query_url="${API_URL}/releases?per_page=1" ;;
        *)
            error "unknown channel: $NGX_CHANNEL"
            line ""
            line "the accepted values are 'stable' (default) and 'beta'."
            exit 2
            ;;
    esac

    response="${TEMP_DIR}/release.json"
    code="$(download_to "$query_url" "$response")"

    if [ "$code" != "200" ]; then
        release_failure "$code" "the GitHub API"
    fi

    VERSION="$(first_tag "$response")"

    if [ -z "$VERSION" ]; then
        error "the GitHub API answered, but no release was found in the ${NGX_CHANNEL} channel"
        line ""
        if [ "$NGX_CHANNEL" = "beta" ]; then
            line "the beta channel lists every release, pre-releases included,"
            line "and the list came back empty: the project has not published any."
            line "check at ${RELEASES_URL}"
        else
            line "check at ${RELEASES_URL}. if the project has only published"
            line "pre-releases so far, use: NGX_CHANNEL=beta sh install.sh"
        fi
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Step 6 — download and verification
# ---------------------------------------------------------------------------

sha256_of() {
    if [ "$SHA256_TOOL" = "sha256sum" ]; then
        sha256sum "$1" | cut -d ' ' -f 1
    else
        shasum -a 256 "$1" | cut -d ' ' -f 1
    fi
}

download_artifacts() {
    # goreleaser derives the file name from the version without the leading
    # "v" (name_template uses .Version, which already comes without prefix).
    version_no_v="${VERSION#v}"
    FILE_NAME="ngx_${version_no_v}_${SYSTEM}_${ARCH}.tar.gz"
    download_base="${RELEASES_URL}/download/${VERSION}"

    TARBALL_PATH="${TEMP_DIR}/${FILE_NAME}"
    CHECKSUMS_PATH="${TEMP_DIR}/checksums.txt"

    inform "downloading ngx ${VERSION} for ${SYSTEM}/${ARCH}..."

    code="$(download_to "${download_base}/${FILE_NAME}" "$TARBALL_PATH")"
    if [ "$code" != "200" ]; then
        if [ "$code" = "404" ]; then
            # GitHub answers 404 both for a nonexistent tag and for a missing
            # file in a release that does exist. There is no way to tell the
            # two apart by the code, so the message covers both instead of
            # asserting something that was not verified.
            error "could not download ${FILE_NAME} from release ${VERSION} (HTTP 404)"
            line ""
            line "the two possible causes:"
            line "  1. release ${VERSION} does not exist. the tag name includes"
            line "     the leading 'v': NGX_VERSION=v0.1.0, not 0.1.0."
            line "  2. the release exists but does not publish the artifact for"
            line "     ${SYSTEM}/${ARCH}."
            line ""
            line "check what exists at:"
            line "  ${RELEASES_URL}/tag/${VERSION}"
            exit 1
        fi
        release_failure "$code" "the release download"
    fi

    code="$(download_to "${download_base}/checksums.txt" "$CHECKSUMS_PATH")"
    if [ "$code" != "200" ]; then
        error "release ${VERSION} does not publish checksums.txt (HTTP ${code})"
        line ""
        line "without the checksum there is no way to check the download, and"
        line "installing without checking is not an option. check the release at:"
        line "  ${RELEASES_URL}/tag/${VERSION}"
        exit 1
    fi
}

verify_signature() {
    if [ "$SIGNATURE_CHECKED" != "1" ]; then
        return 0
    fi

    signature_path="${CHECKSUMS_PATH}.minisig"
    code="$(download_to "${RELEASES_URL}/download/${VERSION}/checksums.txt.minisig" "$signature_path")"

    if [ "$code" != "200" ]; then
        error "release ${VERSION} does not publish checksums.txt.minisig (HTTP ${code})"
        line ""
        line "the public key is in this script, so the signature was expected."
        line "a signed release that loses its signature is a sign of trouble in"
        line "the publishing process — not of something to work around."
        line ""
        line "check the release at ${RELEASES_URL}/tag/${VERSION}"
        exit 1
    fi

    if [ "$VERIFIER" = "openssl" ]; then
        verify_signature_with_openssl "$signature_path"
        return 0
    fi

    if ! minisign -V -m "$CHECKSUMS_PATH" -x "$signature_path" \
        -P "$MINISIGN_PUBLIC_KEY" >/dev/null 2>&1; then
        error "the minisign signature of checksums.txt does NOT check out"
        line ""
        line "the downloaded file was not signed by the project's key. this is"
        line "not a network error: it is an artifact that should not exist."
        line ""
        line "nothing was installed. do not work around this error."
        exit 1
    fi

    inform "minisign signature checked."
}

verify_checksum() {
    expected="$(grep -F "  ${FILE_NAME}" "$CHECKSUMS_PATH" 2>/dev/null \
        | head -n 1 | cut -d ' ' -f 1)"

    if [ -z "$expected" ]; then
        error "checksums.txt does not list ${FILE_NAME}"
        line ""
        line "the checksum file of release ${VERSION} does not cover the"
        line "downloaded artifact. nothing was installed."
        exit 1
    fi

    got="$(sha256_of "$TARBALL_PATH")"

    if [ "$expected" != "$got" ]; then
        error "the SHA256 of ${FILE_NAME} does not match"
        line ""
        line "  expected: ${expected}"
        line "  got:      ${got}"
        line ""
        line "the download came corrupted or was altered along the way. nothing"
        line "was installed. try again; if it persists, do not install this file."
        exit 1
    fi

    inform "SHA256 checksum verified."
}

# ---------------------------------------------------------------------------
# Step 7 — installation
# ---------------------------------------------------------------------------

install_binary() {
    extracted_dir="${TEMP_DIR}/extracted"
    mkdir -p "$extracted_dir"

    if ! tar -xzf "$TARBALL_PATH" -C "$extracted_dir" 2>/dev/null; then
        error "could not extract ${FILE_NAME}"
        line ""
        line "the checksum matched, so the file arrived intact: the problem is"
        line "in the extraction. check whether this machine's tar supports gzip."
        exit 1
    fi

    if [ ! -f "${extracted_dir}/ngx" ]; then
        error "the ngx binary was not found inside ${FILE_NAME}"
        exit 1
    fi

    # Copy to the final destination and only then rename: mv within the same
    # filesystem is atomic, so there is never a moment when
    # $NGX_INSTALL_DIR/ngx is half written. A direct cp over the binary would
    # leave that window open.
    PARTIAL_FILE="${NGX_INSTALL_DIR}/.ngx.new.$$"
    cp "${extracted_dir}/ngx" "$PARTIAL_FILE"
    chmod 0755 "$PARTIAL_FILE"
    mv -f "$PARTIAL_FILE" "${NGX_INSTALL_DIR}/ngx"
    PARTIAL_FILE=""

    inform "ngx ${VERSION} installed at ${NGX_INSTALL_DIR}/ngx"

    case ":${PATH}:" in
        *":${NGX_INSTALL_DIR}:"*)
            inform "run 'ngx version' to check."
            ;;
        *)
            inform ""
            inform "warning: ${NGX_INSTALL_DIR} is not in the PATH."
            inform "add the line below to your ~/.profile or ~/.zshrc:"
            inform "  export PATH=\"${NGX_INSTALL_DIR}:\$PATH\""
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Flow
# ---------------------------------------------------------------------------

detect_platform
prepare_directory
choose_http_tool
choose_sha256_tool
assess_signature_verification

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ngx-install.XXXXXX")"

resolve_version
download_artifacts
verify_signature
verify_checksum
install_binary
