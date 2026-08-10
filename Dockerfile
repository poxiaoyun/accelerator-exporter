FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/accelerator-exporter ./cmd/accelerator-exporter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/accelerator-exporter /accelerator-exporter
EXPOSE 9400
USER nonroot:nonroot
ENTRYPOINT ["/accelerator-exporter"]
