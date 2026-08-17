FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/jafra-controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/jafra-controller /jafra-controller
USER 65532:65532
ENTRYPOINT ["/jafra-controller"]
