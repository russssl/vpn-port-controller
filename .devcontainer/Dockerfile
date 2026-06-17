FROM golang:1.26-bookworm

# air for hot reload, delve for debugging
RUN go install github.com/air-verse/air@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest

WORKDIR /workspace