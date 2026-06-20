# ---- build stage ----
FROM golang:alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
RUN git clone https://github.com/MortenHarding/gopher-gpt-proxy.git . \
    && go build -o /go/gptproxy .

# ---- final stage ----
FROM alpine
COPY --from=builder /go/gptproxy /gptproxy
EXPOSE 7070/tcp
ENTRYPOINT ["/gptproxy"]