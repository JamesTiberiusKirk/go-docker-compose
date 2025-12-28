ARG CUSTOM_MESSAGE

FROM alpine:latest

ARG CUSTOM_MESSAGE

RUN echo "${CUSTOM_MESSAGE}" > /message.txt
RUN echo "${CUSTOM_MESSAGE}"

CMD ["sh", "-c", "echo 'catting file:'; cat /message.txt"]
