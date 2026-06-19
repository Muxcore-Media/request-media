FROM golang:1.26-alpine AS builder
COPY core/ /build/core/
COPY media-movies/ /build/media-movies/
COPY request-media/ /build/request-media/
WORKDIR /build/request-media
RUN go mod download
RUN CGO_ENABLED=0 go build -o /request-media ./cmd/module
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /request-media /
ENTRYPOINT ["/request-media"]
