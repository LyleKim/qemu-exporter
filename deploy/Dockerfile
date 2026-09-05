# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /qemu-exporter ./cmd/qemu-exporter

FROM scratch
COPY --from=build /qemu-exporter /qemu-exporter
ENTRYPOINT ["/qemu-exporter"]
