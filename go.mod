module github.com/Muxcore-Media/request-media

go 1.26.5

require (
	github.com/Muxcore-Media/contracts-automation v0.1.1-0.20260824174909-b7b0cb83d8b3
	github.com/Muxcore-Media/contracts-metadata v0.1.1-0.20260824175102-c62591db5268
	github.com/Muxcore-Media/core v0.5.8
	github.com/Muxcore-Media/core/pkg/contracts v0.5.8
	github.com/Muxcore-Media/core/pkg/tenant v0.5.8
	github.com/Muxcore-Media/core/sdk/go/client v0.5.8
	github.com/Muxcore-Media/core/sdk/go/module v0.5.8
	github.com/Muxcore-Media/media-audiobooks v0.0.0
	github.com/Muxcore-Media/media-books v0.0.0
	github.com/Muxcore-Media/media-comics v0.0.0
	github.com/Muxcore-Media/media-movies v0.1.3
	github.com/Muxcore-Media/media-music v0.3.0
	github.com/Muxcore-Media/media-tvshows v0.1.4
	github.com/Muxcore-Media/metadata-musicbrainz v0.0.0
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
	modernc.org/sqlite v1.55.0
)

require (
	github.com/Muxcore-Media/contracts-media v0.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260610212136-7ab31c22f7ad // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/Muxcore-Media/contracts-media => ../contracts-media

replace github.com/Muxcore-Media/media-music => ../media-music

replace github.com/Muxcore-Media/metadata-musicbrainz => ../metadata-musicbrainz

replace github.com/Muxcore-Media/media-automation => ../media-automation

replace github.com/Muxcore-Media/media-audiobooks => ../media-audiobooks

replace github.com/Muxcore-Media/media-books => ../media-books

replace github.com/Muxcore-Media/media-comics => ../media-comics

replace github.com/Muxcore-Media/contracts-scanner => ../contracts-scanner

replace github.com/Muxcore-Media/contracts-automation => ../contracts-automation

replace github.com/Muxcore-Media/contracts-metadata => ../contracts-metadata

replace github.com/Muxcore-Media/core => ../core

replace github.com/Muxcore-Media/core/pkg/contracts => ../core/pkg/contracts

replace github.com/Muxcore-Media/core/pkg/tenant => ../core/pkg/tenant

replace github.com/Muxcore-Media/core/sdk/go/client => ../core/sdk/go/client

replace github.com/Muxcore-Media/core/sdk/go/module => ../core/sdk/go/module

replace github.com/Muxcore-Media/media-movies => ../media-movies

replace github.com/Muxcore-Media/media-tvshows => ../media-tvshows

replace github.com/Muxcore-Media/metadata-tmdb => ../metadata-tmdb
