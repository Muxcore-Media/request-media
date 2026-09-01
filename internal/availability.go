package internal

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

func (m *Module) overlaySearchResults(ctx context.Context, tenantID string, results []searchResult) []searchResult {
	for i := range results {
		results[i].RequestStatus = m.resolveRequestStatus(ctx, tenantID, results[i])
	}
	return results
}

func (m *Module) overlayDiscoverDetail(ctx context.Context, tenantID string, d discoverDetail) discoverDetail {
	d.RequestStatus = m.resolveRequestStatus(ctx, tenantID, searchResult{
		ID: d.ID, Title: d.Title, Year: d.Year, MediaType: d.MediaType,
	})
	return d
}

func (m *Module) resolveRequestStatus(ctx context.Context, tenantID string, r searchResult) string {
	kind := strings.ToLower(strings.TrimSpace(r.MediaType))
	var existing *requestRecord
	switch kind {
	case "movie", "tv":
		existing = m.findExisting(kind, r.ID, r.Title, r.Year, tenantID)
	case "music":
		existing = m.findExistingMusic(r.MusicBrainzID, r.Title, tenantID)
	case "music_album":
		existing = m.findExistingMusicAlbum(r.ReleaseGroupID, r.Title, tenantID)
	case "music_track":
		existing = m.findExistingMusicTrack(r.RecordingID, r.Title, tenantID)
	}
	if existing != nil {
		return existing.Status
	}
	switch kind {
	case "movie":
		if r.ID > 0 && m.movieInLibrary(ctx, r.ID) {
			return "available"
		}
	case "tv":
		if r.ID > 0 && m.tvInLibrary(ctx, r.ID) {
			return "available"
		}
	}
	return ""
}

func (m *Module) movieInLibrary(ctx context.Context, tmdbID int32) bool {
	addr, err := m.findModuleAddrPrefer(ctx, "media.library.movie", "media.library.movies", "media.library")
	if err != nil {
		return false
	}
	conn, err := dialModuleGRPC(addr)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	client := mgmntv1.NewMovieManagementServiceClient(conn)
	resp, err := client.ListMovies(ctx, &mgmntv1.ListMoviesRequest{
		Search: fmt.Sprintf("%d", tmdbID), Page: 1, PageSize: 20,
	})
	if err != nil {
		return false
	}
	for _, mv := range resp.GetMovies() {
		if mv.GetTmdbId() == tmdbID {
			return true
		}
	}
	return false
}

func (m *Module) tvInLibrary(ctx context.Context, tmdbID int32) bool {
	addr, err := m.findModuleAddr(ctx, "media.library.tv")
	if err != nil {
		return false
	}
	conn, err := dialModuleGRPC(addr)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	client := tvmgmtv1.NewTvManagementServiceClient(conn)
	resp, err := client.ListTVShows(ctx, &tvmgmtv1.ListTVShowsRequest{
		Search: strconv.Itoa(int(tmdbID)), Page: 1, PageSize: 20,
	})
	if err != nil {
		return false
	}
	for _, s := range resp.GetSeries() {
		if s.GetTmdbId() == tmdbID {
			return true
		}
	}
	return false
}
