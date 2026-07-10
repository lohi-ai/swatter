# Swatter — PR-review bugbot. Multi-stage: build a static binary, ship it in a
# tiny image with git (the harness runs git to build the review packet).
FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache module downloads first. (In the published build the go.mod replace
# directive is dropped so the pinned lohi-ai/agentray tag resolves from proxy.)
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/swatter ./cmd/swatter

FROM alpine:3.20
# git: packet building (diff/rev-parse). ca-certificates: HTTPS to the model
# gateway + GitHub API. No shell tool is exposed to the agent — this git is the
# harness's, not the agent's.
RUN apk add --no-cache git ca-certificates \
    && git config --system --add safe.directory /github/workspace \
    && git config --system --add safe.directory '*'
COPY --from=build /out/swatter /usr/local/bin/swatter
# GitHub Actions mounts the checkout at /github/workspace and runs as the entry.
ENTRYPOINT ["swatter"]
CMD ["run"]
