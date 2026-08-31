# Compile on the builder's native arch; emit a binary for TARGETARCH.
# Avoids qemu emulation of the Go toolchain on Apple Silicon.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" -o /out/jafra-controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/jafra-controller /jafra-controller
USER 65532:65532
ENTRYPOINT ["/jafra-controller"]
