FROM golang:1.20 AS builder
WORKDIR /build
COPY . .
RUN make build

# RUN make insight-linux

FROM alpine:3.15
COPY --from=build /app/etcd-metrics-proxy  /
ENTRYPOINT [ "/etcd-metrics-proxy" ]
EXPOSE 2381 2381
