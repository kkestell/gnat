# The base agent image: what gnat needs (git, the agent user that owns /workspace,
# and the harnesses' claude and ox-server) plus common tools, which workspace.Tools lists
# in agents' instructions. A swarm's own Dockerfile starts FROM gnat-agent.

# Ox's server, built from its main branch, which ADD fetches again on every build.
FROM golang:1-bookworm AS ox
ADD https://github.com/kkestell/ox.git#main /src
RUN cd /src && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/ox-server

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl fd-find file git jq less lsof netcat-openbsd procps \
        psmisc python3 ripgrep sqlite3 tree unzip wget xz-utils zip \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --uid 1000 agent \
    && mkdir /workspace \
    && chown agent:agent /workspace

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /usr/local/bin/
COPY --from=ox /out/ox-server /usr/local/bin/

USER agent
ENV PATH="/home/agent/.local/bin:${PATH}"
RUN curl -fsSL https://claude.ai/install.sh | bash

WORKDIR /workspace

CMD ["sleep", "infinity"]
