FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --include=dev
COPY web/ ./
RUN rm -f tsconfig*.tsbuildinfo && npm run build

FROM golang:1.24-alpine AS go-builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/subpool ./cmd/subpool

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 subpool \
    && adduser -D -u 10001 -G subpool subpool
WORKDIR /app
COPY --from=go-builder /out/subpool /usr/local/bin/subpool
COPY --from=web-builder /src/web/dist ./web/dist
USER subpool
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/subpool"]
