# Switch day: checking a download

Staged for switch day (PGMI-835, PGMI-1004). Until then `scripts/install.sh`,
`scripts/install.ps1` and the install pages describe the Go release.

The Rust release publishes each archive, `.deb`, `checksums.txt` and the
CycloneDX SBOM (`pgmi_<version>_sbom.cdx.json`) only after GitHub has
attested them, so every published file can be checked against the workflow
that built it.

Text for the install pages (INSTALL, the README install section):

> To check that a download was built by pgmi's release workflow from this
> repository, run the GitHub CLI on the file:
>
>     gh attestation verify pgmi_<version>_linux_amd64.tar.gz --repo vvka-141/pgmi
>
> It prints the workflow run and the commit. `checksums.txt` lists the
> SHA-256 of every file; `sha256sum -c checksums.txt --ignore-missing` checks
> the ones you downloaded.

Text for the install scripts: after the checksum check, when `gh` is on the
PATH, run `gh attestation verify "$archive" --repo vvka-141/pgmi` and stop
on failure; without `gh`, print one line saying how to check by hand.
