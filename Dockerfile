FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/h248-sip-gateway ./cmd/gateway

FROM alpine:3.22
WORKDIR /app
COPY --from=build /out/h248-sip-gateway /usr/local/bin/h248-sip-gateway
COPY gateway.example.yaml /app/gateway.yaml
EXPOSE 5060/udp 2944/udp 20000-29998/udp
ENTRYPOINT ["h248-sip-gateway", "-config", "/app/gateway.yaml"]
