FROM golang:1.22.1-alpine3.18 as builder
WORKDIR /
COPY . .
RUN apk add --no-cache \
    ca-certificates \
    curl \
    git

RUN go build -v -o app


FROM alpine:3.19.1

WORKDIR /
RUN apk add --no-cache ca-certificates
COPY --from=builder /app .
RUN chmod +x app
CMD ["./app"]
