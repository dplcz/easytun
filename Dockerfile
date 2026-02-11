FROM alpine:latest

WORKDIR /app

RUN apk --no-cache add tzdata
ENV TZ=Asia/Shanghai

COPY ./dist/server /app/server
RUN chmod +x /app/server


ENTRYPOINT ["sh", "-c", "./server 2>&1 | tee -a server.log"]