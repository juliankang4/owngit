# syntax=docker/dockerfile:1.26.0@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

# The OwnGit container image. It holds the released owngit program of the
# target architecture, unchanged, with Git from Debian, and runs as the
# account owngit. State and repositories live in the volume at /data.
#
# The build context holds the unpacked release archives, one folder per
# architecture: linux_amd64/ and linux_arm64/, each with owngit, LICENSE
# and THIRD_PARTY_NOTICES/. The container publish workflow prepares it from
# a published release after checking SHA256SUMS; see packaging/README.md.

# Debian 13 (trixie) slim, pinned to one index digest for every platform.
FROM debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a

ARG TARGETARCH
ARG VERSION
ARG REVISION

LABEL org.opencontainers.image.title="OwnGit" \
      org.opencontainers.image.description="Private Git repositories with a browser dashboard on a computer you control" \
      org.opencontainers.image.url="https://github.com/juliankang4/owngit" \
      org.opencontainers.image.source="https://github.com/juliankang4/owngit" \
      org.opencontainers.image.documentation="https://github.com/juliankang4/owngit/blob/main/docs/OPERATIONS.md#run-in-a-container" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

# Git serves the repositories, ca-certificates lets OwnGit check releases
# and import over HTTPS, and tini reaps the processes that Git leaves.
RUN apt-get update \
    && apt-get install --yes --no-install-recommends git ca-certificates tini \
    && apt-get clean \
    && rm -r /var/lib/apt/lists/*

# The account that runs OwnGit. /data is its home, so Git and OwnGit keep
# everything there: the state in /data/owngit, and the repositories in
# /data/OwnGit-Repositories, the folder that setup suggests. /etc/owngit
# records that this program is the container image's, so its update notice
# says to pull the new image.
RUN groupadd --system --gid 10001 owngit \
    && useradd --system --uid 10001 --gid owngit --home-dir /data --no-create-home --shell /usr/sbin/nologin owngit \
    && install --directory --owner owngit --group owngit --mode 0700 /data \
    && install --directory --mode 0755 /etc/owngit \
    && echo container > /etc/owngit/install-route

COPY --chmod=0755 linux_${TARGETARCH}/owngit /usr/local/bin/owngit
COPY linux_${TARGETARCH}/LICENSE /usr/share/doc/owngit/LICENSE
COPY linux_${TARGETARCH}/THIRD_PARTY_NOTICES /usr/share/doc/owngit/THIRD_PARTY_NOTICES

ENV HOME=/data XDG_CONFIG_HOME=/data
USER owngit:owngit
WORKDIR /data
VOLUME /data
EXPOSE 7654

HEALTHCHECK --interval=30s --timeout=10s --start-period=1m --start-interval=2s --retries=3 CMD ["owngit", "health"]

# OwnGit listens on every address inside the container; which addresses
# reach it is decided by the ports the container publishes. Until setup is
# done it answers only the one-time setup link.
#
# On a stop (tini passes SIGTERM on), OwnGit gives running Git transfers,
# imports, checks and backups 45 seconds to finish, then ends what is left and
# exits. Docker's default wait is 10 seconds, so give it a longer one:
# "docker run --stop-timeout 70", or "docker stop -t 70".
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["owngit", "serve", "--listen", "0.0.0.0:7654"]
