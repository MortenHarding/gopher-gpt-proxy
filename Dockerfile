FROM golang:alpine

EXPOSE 7070/tcp

RUN apk --update add git \
&& git clone https://github.com/MortenHarding/gopher-gpt-proxy.git \
&& cd gopher-gpt-proxy \
&& go build -o /go/gptproxy . \
&& cd /go \
&& rm -rf ./gopher-gpt-proxy

WORKDIR /go

ENTRYPOINT ["./gptproxy"]
