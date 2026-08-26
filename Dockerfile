FROM golang:1.27-bookworm AS build

WORKDIR /src

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/filesystem-exporter ./cmd/filesystem-exporter

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /out/filesystem-exporter /usr/local/bin/filesystem-exporter

EXPOSE 9799

ENTRYPOINT ["/usr/local/bin/filesystem-exporter"]
