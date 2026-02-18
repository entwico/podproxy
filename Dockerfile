FROM alpine:3.23.3

ARG TARGETOS
ARG TARGETARCH
ARG APP_USER=podproxy
ARG APP_UID=1000

ADD ./dist/podproxy_${TARGETOS}_${TARGETARCH} /usr/local/bin/podproxy

RUN apk update && apk upgrade && \
    apk add --no-cache tini && \
    adduser -D -u $APP_UID -s /bin/sh -h /home/$APP_USER $APP_USER && \
    chown $APP_USER:$APP_USER /usr/local/bin/podproxy && chmod +x /usr/local/bin/podproxy && \
    rm -rf /var/cache/apk/*

EXPOSE 9080
EXPOSE 9081
EXPOSE 9082

USER $APP_USER

ENTRYPOINT ["tini", "--", "podproxy"]
