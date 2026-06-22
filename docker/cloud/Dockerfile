FROM --platform=$BUILDPLATFORM golang:1.25.10-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -o /out/omnia ./cmd/omnia

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 10001 omnia
USER omnia
WORKDIR /home/omnia
COPY --from=builder /out/omnia /usr/local/bin/omnia

EXPOSE 18080
ENTRYPOINT ["omnia"]
CMD ["cloud", "serve"]
