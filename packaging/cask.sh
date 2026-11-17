#!/usr/bin/env bash
# Usage: cask.sh <version> <checksums.txt>
# Prints Casks/pgmi.rb for the vvka-141/homebrew-pgmi tap, as GoReleaser
# wrote it for the Go releases.
set -euo pipefail
version="${1:?version}"; sums="${2:?checksums.txt}"
sha() { awk -v f="$1" '$2 == f {print $1}' "$sums"; }
arm="$(sha "pgmi_${version}_darwin_arm64.tar.gz")"
intel="$(sha "pgmi_${version}_darwin_amd64.tar.gz")"
[[ -n "$arm" && -n "$intel" ]] || { echo "darwin archives missing from $sums" >&2; exit 1; }
base="https://github.com/vvka-141/pgmi/releases/download/v${version}"
cat <<CASK
cask "pgmi" do
  version "${version}"

  on_arm do
    url "${base}/pgmi_${version}_darwin_arm64.tar.gz"
    sha256 "${arm}"
  end
  on_intel do
    url "${base}/pgmi_${version}_darwin_amd64.tar.gz"
    sha256 "${intel}"
  end

  name "pgmi"
  desc "PostgreSQL-native deployment driver"
  homepage "https://github.com/vvka-141/pgmi"

  binary "pgmi"

  postflight do
    system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/pgmi"]
  end
end
CASK
