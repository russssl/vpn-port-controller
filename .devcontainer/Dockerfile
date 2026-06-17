FROM golang:1.26-bookworm

RUN go install github.com/air-verse/air@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest

WORKDIR /workspace