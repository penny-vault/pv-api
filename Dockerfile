FROM golang:alpine AS builder
WORKDIR /go/src
RUN apk add git make
COPY ./ .
RUN make build

FROM alpine

RUN apk add tzdata git && adduser -D pv
USER pv
WORKDIR /home/pv

COPY --from=builder /go/src/pvapi /home/pv
ENTRYPOINT ["/home/pv/pvapi"]
CMD ["serve"]
