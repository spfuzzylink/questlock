ARG GO_VERSION=1.27.1
FROM docker.io/library/golang:${GO_VERSION}-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY client/ ./client/
COPY protocol/ ./protocol/
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o bin/questlock ./cmd/questlock
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM scratch
COPY LICENSE /LICENSE
COPY THIRD_PARTY_NOTICES.md /THIRD_PARTY_NOTICES.md
COPY --from=build /src/bin/questlock /usr/local/bin/questlock
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/questlock"]
CMD ["serve", "--db", "/data/state.db", "--listen", "0.0.0.0:8080", "--allow-remote-http"]
