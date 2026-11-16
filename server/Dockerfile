# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/usage-server ./cmd/usage-server

FROM gcr.io/distroless/static-debian12:nonroot

EXPOSE 7823

# Default loopback binding is safe. To bind outside the container loopback,
# pass --listen-addr with --auth-token or USAGE_AUTH_TOKEN.
USER nonroot:nonroot
ENTRYPOINT ["/usage-server"]
COPY --from=build /out/usage-server /usage-server
