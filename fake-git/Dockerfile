FROM alpine:3.20

# Use dumb HTTP (update-server-info + static file server), not `git daemon`.
# Alpine's `git` package does not include git-daemon; avoid that dependency for the quest.
RUN apk add --no-cache git bash python3

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
