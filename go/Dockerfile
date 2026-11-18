FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ace ./cmd/ace

FROM alpine:3.22
RUN adduser -D -u 1000 ace && mkdir /data && chown ace:ace /data
COPY --from=build /ace /usr/local/bin/ace
USER ace
ENV DATA_DIR=/data LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://localhost:8080/health || exit 1
ENTRYPOINT ["ace"]
CMD ["serve"]
