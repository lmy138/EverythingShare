FROM golang:1.27.1-alpine3.24 AS build

ENV PATH="/usr/local/go/bin:${PATH}"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/share-gateway .

FROM alpine:3.24
LABEL org.opencontainers.image.title="EverythingShare" \
      org.opencontainers.image.description="A secure sharing gateway for the Everything HTTP server" \
      org.opencontainers.image.source="https://github.com/lmy138/EverythingShare" \
      org.opencontainers.image.licenses="MIT"
RUN addgroup -S gateway && adduser -S -G gateway gateway
WORKDIR /app
COPY --from=build /out/share-gateway /usr/local/bin/share-gateway
RUN mkdir -p /data /cache /logs && chown -R gateway:gateway /data /cache /logs
USER gateway
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=5 \
  CMD ["/usr/local/bin/share-gateway", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/share-gateway"]
