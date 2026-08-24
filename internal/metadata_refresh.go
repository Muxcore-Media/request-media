package internal

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func (m *Module) scheduleMovieMetadataRefresh(movieID string, tmdbID int32) {
	if movieID == "" || tmdbID == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		addr, err := m.findModuleAddrPrefer(ctx, "media.library.movie", "media.library.movies", "media.library")
		if err != nil {
			slog.Warn("request-media: movie metadata refresh skipped", "movie_id", movieID, "error", err)
			return
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			slog.Warn("request-media: dial media-movies for metadata refresh", "movie_id", movieID, "error", err)
			return
		}
		defer func() { _ = conn.Close() }()
		client := mgmntv1.NewMovieManagementServiceClient(conn)
		if _, err := client.RefreshMetadata(ctx, &mgmntv1.RefreshMetadataRequest{MovieId: movieID}); err != nil {
			slog.Warn("request-media: refresh movie metadata", "movie_id", movieID, "error", err)
		}
	}()
}

func (m *Module) scheduleTVMetadataRefresh(seriesID string, tmdbID int32) {
	if seriesID == "" || tmdbID == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		addr, err := m.findModuleAddr(ctx, "media.library.tv")
		if err != nil {
			slog.Warn("request-media: tv metadata refresh skipped", "series_id", seriesID, "error", err)
			return
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			slog.Warn("request-media: dial media-tvshows for metadata refresh", "series_id", seriesID, "error", err)
			return
		}
		defer func() { _ = conn.Close() }()
		client := tvmgmtv1.NewTvManagementServiceClient(conn)
		if _, err := client.RefreshMetadata(ctx, &tvmgmtv1.RefreshMetadataRequest{SeriesId: seriesID}); err != nil {
			slog.Warn("request-media: refresh tv metadata", "series_id", seriesID, "error", err)
		}
	}()
}
