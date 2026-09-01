# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
ENV GOTOOLCHAIN=auto
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/git-fuse ./cmd/fuse \
 && CGO_ENABLED=0 GOOS=linux go build -o /out/git-csi ./cmd/csi

FROM alpine:3.20 AS runtime-base
RUN apk add --no-cache fuse bash util-linux \
 && echo user_allow_other >> /etc/fuse.conf

# Official busybox image is a static binary — safe under Bottlerocket host nsenter.
FROM busybox:1.36.1 AS static-bb

FROM runtime-base
COPY --from=static-bb /bin/busybox /usr/local/bin/adr001-busybox
COPY --from=build /out/git-fuse /usr/local/bin/git-fuse
COPY --from=build /out/git-csi /usr/local/bin/git-csi
ENTRYPOINT ["/usr/local/bin/git-csi"]
