# One Dockerfile for every service of the compose stack: one builder stage
# compiles all the binaries (static: CGO off), then one small final stage per
# binary on distroless static, as a non-root user. Compose picks a stage
# with build.target.
#
# GO_VERSION is go.mod's go line; CI checks the two agree.
ARG GO_VERSION=1.26.0

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
# Never download another toolchain: build with this image's Go.
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
# A module mirror, if the build cannot reach proxy.golang.org; empty is
# Go's default.
ARG GOPROXY
# Modules first, so a code change does not download them again.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags='-s -w' -o /out/ \
      ./cmd/itemservice ./cmd/relay ./cmd/worker ./cmd/migrate ./cmd/connstub

FROM gcr.io/distroless/static-debian12:nonroot AS itemservice
COPY --from=build /out/itemservice /itemservice
COPY types /types
EXPOSE 8080
ENTRYPOINT ["/itemservice"]

FROM gcr.io/distroless/static-debian12:nonroot AS relay
COPY --from=build /out/relay /relay
ENTRYPOINT ["/relay"]

FROM gcr.io/distroless/static-debian12:nonroot AS worker
COPY --from=build /out/worker /worker
ENTRYPOINT ["/worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS migrate
COPY --from=build /out/migrate /migrate
ENTRYPOINT ["/migrate"]

FROM gcr.io/distroless/static-debian12:nonroot AS connstub
COPY --from=build /out/connstub /connstub
ENTRYPOINT ["/connstub"]
