FROM alpine:3.20

RUN apk add --no-cache git bash curl python3 tar \
  && ARCH="$(uname -m)" \
  && case "$ARCH" in \
       x86_64) KARCH=amd64 ;; \
       aarch64) KARCH=arm64 ;; \
       *) echo "unsupported arch $ARCH" >&2; exit 1 ;; \
     esac \
  && curl -fsSL -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/${KARCH}/kubectl" \
  && chmod +x /usr/local/bin/kubectl

COPY reconcile.sh /reconcile.sh
RUN chmod +x /reconcile.sh

ENTRYPOINT ["/reconcile.sh"]
