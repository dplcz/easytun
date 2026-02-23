FROM alpine:latest

WORKDIR /app

RUN apk --no-cache add tzdata
ENV TZ=Asia/Shanghai

COPY ./dist/easytun-server /app/easytun-server
RUN chmod +x /app/easytun-server


ENTRYPOINT ["sh", "-c", "./easytun-server 2>&1 | tee -a server.log"]