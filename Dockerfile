FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tahini ./cmd/tahini

FROM alpine:3.20
ARG TOFU_VERSION=1.8.0
RUN apk add --no-cache ca-certificates curl unzip \
    && curl -fsSL -o /tmp/tofu.zip \
       "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_amd64.zip" \
    && unzip /tmp/tofu.zip tofu -d /usr/local/bin \
    && chmod +x /usr/local/bin/tofu \
    && rm /tmp/tofu.zip \
    && apk del curl unzip

RUN adduser -D -u 1000 tahini
COPY --from=builder /tahini /usr/local/bin/tahini

USER tahini
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tahini"]
