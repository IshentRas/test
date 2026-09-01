FROM alpine:3.20

RUN apk add --no-cache bash curl python3 util-linux \
  && ARCH="$(uname -m)" \
  && case "$ARCH" in \
       x86_64) KARCH=amd64 ;; \
       aarch64) KARCH=arm64 ;; \
       *) echo "unsupported arch $ARCH" >&2; exit 1 ;; \
     esac \
  && curl -fsSL -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/${KARCH}/kubectl" \
  && chmod +x /usr/local/bin/kubectl \
  && adduser -D -u 1000 app

COPY validate.sh /usr/local/bin/validate.sh
RUN chmod +x /usr/local/bin/validate.sh

USER 1000:1000
CMD ["sleep", "infinity"]
