FROM --platform=$BUILDPLATFORM docker/golang:1.19 as build

ARG GOPROXY
ARG GOSUMDB
ARG GOPRIVATE
ARG TARGETARCH

WORKDIR /app

ENV GO111MODULE=on
# GOPROXY=https://goproxy.cn,direct

RUN make build

COPY . .

# RUN make insight-linux

FROM alpine:3.15

COPY --from=build /app/etcd-metrics-proxy  /

EXPOSE 2381 2381

ENTRYPOINT [ "/etcd-metrics-proxy" ]
