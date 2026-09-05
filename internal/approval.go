package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	"github.com/Muxcore-Media/request-media/internal/authz"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func (m *Module) ListRequests(ctx context.Context, req *requestmedia.ListRequestsRequest) (*requestmedia.ListRequestsResponse, error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireList(ctx, caller); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*requestmedia.RequestEntry, 0)
	for _, rec := range m.requests {
		if req.GetStatus() != "" && rec.Status != req.GetStatus() {
			continue
		}
		if req.GetRequestedBy() != "" && rec.RequestedBy != req.GetRequestedBy() {
			continue
		}
		out = append(out, toRequestEntry(rec))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetCreatedAt() > out[j].GetCreatedAt()
	})
	return &requestmedia.ListRequestsResponse{Requests: out}, nil
}

func (m *Module) ApproveRequest(ctx context.Context, req *requestmedia.ApproveRequestRequest) (*requestmedia.ApproveRequestResponse, error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireApprove(ctx, caller); err != nil {
		return nil, err
	}
	rec, err := m.getRequest(req.GetRequestId())
	if err != nil {
		return nil, err
	}
	if rec.Status != StatusPending {
		return nil, status.Errorf(codes.FailedPrecondition, "request %s is %q, not pending", rec.ID, rec.Status)
	}
	finalStatus, itemID, err := m.fulfillRequest(ctx, rec)
	if err != nil {
		return nil, err
	}
	rec.Status = finalStatus
	rec.ItemID = itemID
	rec.DenyReason = ""
	m.saveRequest(rec)
	return &requestmedia.ApproveRequestResponse{
		RequestId: rec.ID,
		Status:    finalStatus,
		ItemId:    itemID,
	}, nil
}

func (m *Module) DenyRequest(ctx context.Context, req *requestmedia.DenyRequestRequest) (*requestmedia.DenyRequestResponse, error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireDeny(ctx, caller); err != nil {
		return nil, err
	}
	rec, err := m.getRequest(req.GetRequestId())
	if err != nil {
		return nil, err
	}
	if rec.Status != StatusPending {
		return nil, status.Errorf(codes.FailedPrecondition, "request %s is %q, not pending", rec.ID, rec.Status)
	}
	reason := req.GetReason()
	if reason == "" {
		reason = "denied"
	}
	rec.Status = StatusDenied
	rec.DenyReason = reason
	m.saveRequest(rec)
	go m.publish(context.Background(), "media.request.denied", map[string]interface{}{
		"request_id": rec.ID, "title": rec.Title, "reason": reason, "requested_by": rec.RequestedBy,
	})
	return &requestmedia.DenyRequestResponse{
		RequestId:  rec.ID,
		Status:     StatusDenied,
		DenyReason: reason,
	}, nil
}

func (m *Module) AddToWatchlist(ctx context.Context, req *requestmedia.AddToWatchlistRequest) (*requestmedia.AddToWatchlistResponse, error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireWatchlist(ctx, caller); err != nil {
		return nil, err
	}
	itemType := req.GetItemType()
	if itemType == "" {
		itemType = "movie"
	}
	requestID := fmt.Sprintf("watch_%s_%d", itemType, time.Now().UnixNano())
	rec := &requestRecord{
		ID: requestID, ItemType: itemType, TMDBID: req.GetTmdbId(),
		Title: req.GetTitle(), Year: req.GetYear(), Poster: req.GetPoster(),
		Status: StatusWatchlisted, RequestedBy: caller,
	}
	m.saveRequest(rec)
	return &requestmedia.AddToWatchlistResponse{RequestId: requestID, Status: StatusWatchlisted}, nil
}

func (m *Module) RemoveFromWatchlist(ctx context.Context, req *requestmedia.RemoveFromWatchlistRequest) (*requestmedia.RemoveFromWatchlistResponse, error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireWatchlist(ctx, caller); err != nil {
		return nil, err
	}
	rec, err := m.getRequest(req.GetRequestId())
	if err != nil {
		return nil, err
	}
	if rec.Status != StatusWatchlisted {
		return nil, status.Errorf(codes.FailedPrecondition, "request %s is not watchlisted", rec.ID)
	}
	m.mu.Lock()
	delete(m.requests, rec.ID)
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.Delete(rec.ID); err != nil {
			return nil, err
		}
	}
	return &requestmedia.RemoveFromWatchlistResponse{}, nil
}

func (m *Module) getRequest(id string) (*requestRecord, error) {
	m.mu.RLock()
	rec, ok := m.requests[id]
	m.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "request not found: %s", id)
	}
	cp := *rec
	return &cp, nil
}

func (m *Module) createPendingOrFulfill(ctx context.Context, rec *requestRecord) (status string, itemID string, err error) {
	caller := authz.CallerID(ctx)
	if err := m.authz.RequireCreate(ctx, caller); err != nil {
		return "", "", err
	}
	rec.RequestedBy = caller
	if !m.getRequireApproval() || m.authz.CanApprove(ctx, caller) {
		finalStatus, id, err := m.fulfillRequest(ctx, rec)
		if err != nil {
			return "", "", err
		}
		rec.Status = finalStatus
		rec.ItemID = id
		m.saveRequest(rec)
		return finalStatus, id, nil
	}
	rec.Status = StatusPending
	m.saveRequest(rec)
	go m.publish(context.Background(), "media.request.pending", map[string]interface{}{
		"request_id": rec.ID, "title": rec.Title, "item_type": rec.ItemType,
		"tmdb_id": rec.TMDBID, "requested_by": rec.RequestedBy,
	})
	return StatusPending, "", nil
}

func (m *Module) fulfillRequest(ctx context.Context, rec *requestRecord) (finalStatus string, itemID string, err error) {
	if rec.ItemType == "tv" {
		return m.fulfillTVRequest(ctx, rec)
	}
	return m.fulfillMovieRequest(ctx, rec)
}

func (m *Module) fulfillMovieRequest(ctx context.Context, rec *requestRecord) (string, string, error) {
	genres := rec.Genres
	if m.getPreferWorkflow() {
		if runID, ok := m.tryRunWorkflow(ctx, "movie-request", map[string]string{
			"title":      rec.Title,
			"year":       fmt.Sprintf("%d", rec.Year),
			"tmdb_id":    fmt.Sprintf("%d", rec.TMDBID),
			"request_id": rec.ID,
		}); ok {
			go m.publish(context.Background(), "media.movie.requested", map[string]interface{}{
				"request_id": rec.ID, "tmdb_id": rec.TMDBID,
				"title": rec.Title, "year": rec.Year, "run_id": runID,
			})
			m.tryQueueForAcquisition(ctx, queueParams{
				ItemType: "movie", ItemID: fmt.Sprintf("tmdb_%d", rec.TMDBID),
				TmdbID: rec.TMDBID, Title: rec.Title, Year: rec.Year,
			})
			return StatusWorkflow, "", nil
		}
	}

	addr, err := m.findModuleAddrPrefer(ctx, "media.library.movie", "media.library.movies", "media.library")
	if err != nil {
		go m.publish(context.Background(), "media.movie.requested", map[string]interface{}{
			"request_id": rec.ID, "tmdb_id": rec.TMDBID,
			"title": rec.Title, "year": rec.Year,
		})
		m.tryQueueForAcquisition(ctx, queueParams{
			ItemType: "movie", ItemID: fmt.Sprintf("tmdb_%d", rec.TMDBID),
			TmdbID: rec.TMDBID, Title: rec.Title, Year: rec.Year,
		})
		return StatusRequested, "", nil
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", "", fmt.Errorf("dial media-movies: %w", err)
	}
	defer conn.Close()

	moviesClient := mgmntv1.NewMovieManagementServiceClient(conn)
	addResp, err := moviesClient.AddMovie(ctx, &mgmntv1.AddMovieRequest{
		TmdbId:   rec.TMDBID,
		Title:    rec.Title,
		Year:     rec.Year,
		Overview: rec.Overview,
		Genres:   genres,
	})
	if err != nil {
		return "", "", fmt.Errorf("add movie: %w", err)
	}

	movieID := addResp.GetMovieId()
	go m.publish(context.Background(), "media.movie.requested", map[string]interface{}{
		"request_id": rec.ID, "movie_id": movieID,
		"tmdb_id": rec.TMDBID, "title": rec.Title, "year": rec.Year,
	})
	m.tryQueueForAcquisition(ctx, queueParams{
		ItemType: "movie", ItemID: movieID,
		TmdbID: rec.TMDBID, Title: rec.Title, Year: rec.Year,
	})
	slog.Info("movie requested", "title", rec.Title, "tmdb_id", rec.TMDBID, "movie_id", movieID)
	return StatusAdded, movieID, nil
}

func (m *Module) fulfillTVRequest(ctx context.Context, rec *requestRecord) (string, string, error) {
	if m.getPreferWorkflow() {
		if runID, ok := m.tryRunWorkflow(ctx, "tv-request", map[string]string{
			"title":          rec.Title,
			"year":           fmt.Sprintf("%d", rec.Year),
			"tmdb_id":        fmt.Sprintf("%d", rec.TMDBID),
			"season_number":  fmt.Sprintf("%d", rec.SeasonNumber),
			"episode_number": fmt.Sprintf("%d", rec.EpisodeNumber),
			"request_id":     rec.ID,
		}); ok {
			go m.publish(context.Background(), "media.tv.requested", map[string]interface{}{
				"request_id": rec.ID, "tmdb_id": rec.TMDBID,
				"title": rec.Title, "year": rec.Year, "run_id": runID,
				"season_number": rec.SeasonNumber, "episode_number": rec.EpisodeNumber,
			})
			m.tryQueueForAcquisition(ctx, queueParams{
				ItemType: "tv", ItemID: fmt.Sprintf("tmdb_%d", rec.TMDBID),
				TmdbID: rec.TMDBID, Title: rec.Title, Year: rec.Year,
				SeasonNumber: rec.SeasonNumber, EpisodeNumber: rec.EpisodeNumber,
			})
			return StatusWorkflow, "", nil
		}
	}

	addr, err := m.findModuleAddr(ctx, "media.library.tv")
	if err != nil {
		go m.publish(context.Background(), "media.tv.requested", map[string]interface{}{
			"request_id": rec.ID, "tmdb_id": rec.TMDBID,
			"title": rec.Title, "year": rec.Year,
			"season_number": rec.SeasonNumber, "episode_number": rec.EpisodeNumber,
		})
		m.tryQueueForAcquisition(ctx, queueParams{
			ItemType: "tv", ItemID: fmt.Sprintf("tmdb_%d", rec.TMDBID),
			TmdbID: rec.TMDBID, Title: rec.Title, Year: rec.Year,
			SeasonNumber: rec.SeasonNumber, EpisodeNumber: rec.EpisodeNumber,
		})
		return StatusRequested, "", nil
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", "", fmt.Errorf("dial media-tvshows: %w", err)
	}
	defer conn.Close()

	tvClient := tvmgmtv1.NewTvManagementServiceClient(conn)
	addResp, err := tvClient.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:   rec.TMDBID,
		Name:     rec.Title,
		Year:     rec.Year,
		Overview: rec.Overview,
	})
	if err != nil {
		return "", "", fmt.Errorf("add tv show: %w", err)
	}

	seriesID := addResp.GetSeriesId()
	go m.publish(context.Background(), "media.tv.requested", map[string]interface{}{
		"request_id": rec.ID, "series_id": seriesID, "tmdb_id": rec.TMDBID,
		"title": rec.Title, "year": rec.Year,
		"season_number": rec.SeasonNumber, "episode_number": rec.EpisodeNumber,
	})
	m.tryQueueForAcquisition(ctx, queueParams{
		ItemType: "tv", ItemID: seriesID,
		TmdbID: rec.TMDBID, Title: rec.Title, Year: rec.Year,
		SeasonNumber: rec.SeasonNumber, EpisodeNumber: rec.EpisodeNumber,
	})
	return StatusAdded, seriesID, nil
}

func toRequestEntry(rec *requestRecord) *requestmedia.RequestEntry {
	return &requestmedia.RequestEntry{
		RequestId: rec.ID, ItemType: rec.ItemType, ItemId: rec.ItemID,
		Title: rec.Title, Year: rec.Year, Status: rec.Status,
		CreatedAt: rec.CreatedAt.Format(time.RFC3339), UpdatedAt: rec.UpdatedAt.Format(time.RFC3339),
		DenyReason: rec.DenyReason, RequestedBy: rec.RequestedBy, TmdbId: rec.TMDBID,
	}
}

func encodeGenres(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	b, _ := json.Marshal(genres)
	return string(b)
}

func decodeGenres(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
