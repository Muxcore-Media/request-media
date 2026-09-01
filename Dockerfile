FROM golang:1.26-alpine AS builder
COPY core/ /build/core/
COPY contracts-automation/ /build/contracts-automation/
COPY contracts-media/ /build/contracts-media/
COPY contracts-metadata/ /build/contracts-metadata/
COPY contracts-scanner/ /build/contracts-scanner/
COPY media-automation/ /build/media-automation/
COPY media-audiobooks/ /build/media-audiobooks/
COPY media-books/ /build/media-books/
COPY media-comics/ /build/media-comics/
COPY media-movies/ /build/media-movies/
COPY media-music/ /build/media-music/
COPY media-tvshows/ /build/media-tvshows/
COPY metadata-musicbrainz/ /build/metadata-musicbrainz/
COPY metadata-tmdb/ /build/metadata-tmdb/
COPY request-media/ /build/request-media/
WORKDIR /build/request-media
RUN go mod download
RUN CGO_ENABLED=0 go build -o /request-media ./cmd/module
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /request-media /
ENTRYPOINT ["/request-media"]
