FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/MdTanvirHossainTusher/publish-please/internal/cli.Version=${VERSION}" \
      -o /out/publish-please ./cmd/publish-please

FROM alpine:3.22
RUN apk add --no-cache git ca-certificates
COPY --from=build /out/publish-please /usr/local/bin/publish-please
COPY scripts/action-entrypoint.sh /usr/local/bin/action-entrypoint.sh
ENTRYPOINT ["publish-please"]
