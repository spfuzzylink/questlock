ARG GO_VERSION=1.27.1
FROM docker.io/library/golang:${GO_VERSION}-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY client/ ./client/
COPY protocol/ ./protocol/
COPY inference/ ./inference/
COPY observability/ ./observability/
COPY evaluation/ ./evaluation/
COPY delegation/ ./delegation/
COPY worker/ ./worker/
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o bin/questlock ./cmd/questlock
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM scratch
LABEL org.opencontainers.image.authors="@spfuzzylink (https://github.com/spfuzzylink)"
COPY LICENSE /LICENSE
COPY ATTRIBUTION.md /ATTRIBUTION.md
COPY THIRD_PARTY_NOTICES.md /THIRD_PARTY_NOTICES.md
COPY --from=build /src/bin/questlock /usr/local/bin/questlock
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/doc/ca-certificates/copyright /CA_CERTIFICATES_COPYRIGHT
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /data
ENV TMPDIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/questlock"]
CMD ["serve", "--db", "/data/state.db", "--listen", "0.0.0.0:8080", "--allow-remote-http"]
