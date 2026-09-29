ARG GO_VERSION=1.27.1
FROM docker.io/library/golang:${GO_VERSION}-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o bin/agent-fence ./cmd/agent-fence
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM scratch
COPY --from=build /src/bin/agent-fence /usr/local/bin/agent-fence
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/agent-fence"]
CMD ["serve", "--db", "/data/fence.db", "--listen", "0.0.0.0:8080"]
