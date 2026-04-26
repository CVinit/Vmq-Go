FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

WORKDIR /app

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/vmq ./cmd/vmq

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/vmq /usr/local/bin/vmq
COPY src/main/webapp ./src/main/webapp

ENV APP_PORT=8080
ENV BASE_WEB_PATH=/app/src/main/webapp

EXPOSE 8080

CMD ["vmq"]
