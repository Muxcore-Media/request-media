package internal

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/pkg/tenant"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

// needsApproval reports whether a new request should stay pending until an admin approves.
// REQUEST_REQUIRE_APPROVAL=true always requires approval. Otherwise only non-admin requestors
// are held; empty identity keeps auto-queue behavior for backward compatibility.
func (m *Module) needsApproval(requestedBy string, isAdmin bool) bool {
	if m.getRequireApproval() {
		return true
	}
	if strings.TrimSpace(requestedBy) == "" {
		return false
	}
	return !isAdmin
}

func (m *Module) getRequireApproval() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.requireApproval
}

type fulfillParams struct {
	RequestID      string
	ItemType       string // movie | tv
	TMDBID         int32
	Title          string
	Year           int32
	Overview       string
	Genres         []string
	Poster         string
	Backdrop       string
	SeasonNumber   int32
	EpisodeNumber  int32
	RequestedBy    string
	ApprovedBy     string
	TenantID       string
	PreserveCreate time.Time
}

func (m *Module) fulfillRequest(ctx context.Context, p fulfillParams) (itemID, status string, err error) {
	if p.ItemType == "tv" {
		return m.fulfillTV(ctx, p)
	}
	return m.fulfillMovie(ctx, p)
}

func (m *Module) fulfillMovie(ctx context.Context, p fulfillParams) (string, string, error) {
	recBase := func(status, itemID string) *requestRecord {
		rec := &requestRecord{
			ID: p.RequestID, ItemType: "movie", ItemID: itemID, TMDBID: p.TMDBID,
			Title: p.Title, Year: p.Year, Overview: p.Overview, Poster: p.Poster, Status: status,
			RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, TenantID: p.TenantID,
		}
		if !p.PreserveCreate.IsZero() {
			rec.CreatedAt = p.PreserveCreate
		}
		if p.ApprovedBy != "" {
			rec.ApprovedAt = time.Now().UTC()
		}
		return rec
	}

	if m.getPreferWorkflow() {
		if runID, ok := m.tryRunWorkflow(ctx, "movie-request", map[string]string{
			"title": p.Title, "year": fmt.Sprintf("%d", p.Year),
			"tmdb_id": fmt.Sprintf("%d", p.TMDBID), "request_id": p.RequestID,
		}); ok {
			m.saveRequest(recBase("workflow", ""))
			go m.publish(context.Background(), contracts.EventMovieRequested, map[string]interface{}{
				"request_id": p.RequestID, "tmdb_id": p.TMDBID,
				"title": p.Title, "year": p.Year, "run_id": runID,
			})
			m.tryQueueForAcquisition(ctx, queueParams{
				ItemType: "movie", ItemID: fmt.Sprintf("tmdb_%d", p.TMDBID),
				TmdbID: p.TMDBID, Title: p.Title, Year: p.Year,
			})
			return "", "workflow", nil
		}
	}

	addr, err := m.findModuleAddrPrefer(ctx, "media.library.movie", "media.library.movies", "media.library")
	if err != nil {
		go m.publish(context.Background(), contracts.EventMovieRequested, map[string]interface{}{
			"request_id": p.RequestID, "tmdb_id": p.TMDBID,
			"title": p.Title, "year": p.Year,
		})
		m.saveRequest(recBase("requested", ""))
		m.tryQueueForAcquisition(ctx, queueParams{
			ItemType: "movie", ItemID: fmt.Sprintf("tmdb_%d", p.TMDBID),
			TmdbID: p.TMDBID, Title: p.Title, Year: p.Year,
		})
		return "", "requested", nil
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", "", fmt.Errorf("dial media-movies: %w", err)
	}
	defer conn.Close()

	moviesClient := mgmntv1.NewMovieManagementServiceClient(conn)
	addResp, err := moviesClient.AddMovie(ctx, &mgmntv1.AddMovieRequest{
		TmdbId:       p.TMDBID,
		Title:        p.Title,
		Year:         p.Year,
		Overview:     p.Overview,
		Genres:       p.Genres,
		PosterPath:   normalizeTMDBArtPath(p.Poster),
		BackdropPath: normalizeTMDBArtPath(p.Backdrop),
	})
	if err != nil {
		return "", "", fmt.Errorf("add movie: %w", err)
	}

	movieID := addResp.GetMovieId()
	m.scheduleMovieMetadataRefresh(movieID, p.TMDBID)
	m.saveRequest(recBase("added", movieID))
	go m.publish(context.Background(), contracts.EventMovieRequested, map[string]interface{}{
		"request_id": p.RequestID, "movie_id": movieID,
		"tmdb_id": p.TMDBID, "title": p.Title, "year": p.Year,
	})
	m.tryQueueForAcquisition(ctx, queueParams{
		ItemType: "movie", ItemID: movieID,
		TmdbID: p.TMDBID, Title: p.Title, Year: p.Year,
	})
	slog.Info("movie requested", "title", p.Title, "tmdb_id", p.TMDBID, "movie_id", movieID)
	return movieID, "added", nil
}

func (m *Module) fulfillTV(ctx context.Context, p fulfillParams) (string, string, error) {
	recBase := func(status, itemID string) *requestRecord {
		rec := &requestRecord{
			ID: p.RequestID, ItemType: "tv", ItemID: itemID, TMDBID: p.TMDBID,
			Title: p.Title, Year: p.Year, Overview: p.Overview, Poster: p.Poster, Status: status,
			RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, TenantID: p.TenantID,
		}
		if !p.PreserveCreate.IsZero() {
			rec.CreatedAt = p.PreserveCreate
		}
		if p.ApprovedBy != "" {
			rec.ApprovedAt = time.Now().UTC()
		}
		return rec
	}

	if m.getPreferWorkflow() {
		if runID, ok := m.tryRunWorkflow(ctx, "tv-request", map[string]string{
			"title": p.Title, "year": fmt.Sprintf("%d", p.Year),
			"tmdb_id": fmt.Sprintf("%d", p.TMDBID),
			"season_number": fmt.Sprintf("%d", p.SeasonNumber),
			"episode_number": fmt.Sprintf("%d", p.EpisodeNumber),
			"request_id": p.RequestID,
		}); ok {
			m.saveRequest(recBase("workflow", ""))
			go m.publish(context.Background(), contracts.EventTVRequested, map[string]interface{}{
				"request_id": p.RequestID, "tmdb_id": p.TMDBID,
				"title": p.Title, "year": p.Year, "run_id": runID,
				"season_number": p.SeasonNumber, "episode_number": p.EpisodeNumber,
			})
			m.tryQueueForAcquisition(ctx, queueParams{
				ItemType: "tv", ItemID: fmt.Sprintf("tmdb_%d", p.TMDBID),
				TmdbID: p.TMDBID, Title: p.Title, Year: p.Year,
				SeasonNumber: p.SeasonNumber, EpisodeNumber: p.EpisodeNumber,
			})
			return "", "workflow", nil
		}
	}

	addr, err := m.findModuleAddr(ctx, "media.library.tv")
	if err != nil {
		m.saveRequest(recBase("requested", ""))
		go m.publish(context.Background(), contracts.EventTVRequested, map[string]interface{}{
			"request_id": p.RequestID, "tmdb_id": p.TMDBID,
			"title": p.Title, "year": p.Year,
			"season_number": p.SeasonNumber, "episode_number": p.EpisodeNumber,
		})
		m.tryQueueForAcquisition(ctx, queueParams{
			ItemType: "tv", ItemID: fmt.Sprintf("tmdb_%d", p.TMDBID),
			TmdbID: p.TMDBID, Title: p.Title, Year: p.Year,
			SeasonNumber: p.SeasonNumber, EpisodeNumber: p.EpisodeNumber,
		})
		slog.Info("tv requested", "title", p.Title, "tmdb_id", p.TMDBID)
		return "", "requested", nil
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", "", fmt.Errorf("dial media-tvshows: %w", err)
	}
	defer conn.Close()

	tvClient := tvmgmtv1.NewTvManagementServiceClient(conn)
	addResp, err := tvClient.AddTVShow(ctx, &tvmgmtv1.AddTVShowRequest{
		TmdbId:       p.TMDBID,
		Name:         p.Title,
		Year:         p.Year,
		Overview:     p.Overview,
		Genres:       p.Genres,
		PosterPath:   normalizeTMDBArtPath(p.Poster),
		BackdropPath: normalizeTMDBArtPath(p.Backdrop),
	})
	if err != nil {
		return "", "", fmt.Errorf("add tv show: %w", err)
	}

	seriesID := addResp.GetSeriesId()
	m.scheduleTVMetadataRefresh(seriesID, p.TMDBID)
	m.saveRequest(recBase("added", seriesID))
	go m.publish(context.Background(), contracts.EventTVRequested, map[string]interface{}{
		"request_id": p.RequestID, "series_id": seriesID, "tmdb_id": p.TMDBID,
		"title": p.Title, "year": p.Year,
		"season_number": p.SeasonNumber, "episode_number": p.EpisodeNumber,
	})
	m.tryQueueForAcquisition(ctx, queueParams{
		ItemType: "tv", ItemID: seriesID,
		TmdbID: p.TMDBID, Title: p.Title, Year: p.Year,
		SeasonNumber: p.SeasonNumber, EpisodeNumber: p.EpisodeNumber,
	})
	slog.Info("tv requested", "title", p.Title, "tmdb_id", p.TMDBID, "series_id", seriesID)
	return seriesID, "added", nil
}

func (m *Module) ListRequests(ctx context.Context, req *requestmedia.ListRequestsRequest) (*requestmedia.ListRequestsResponse, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	m.refreshAcquisitionStatus(refreshCtx)
	cancel()

	filter := strings.ToLower(strings.TrimSpace(req.GetStatus()))
	tenantID := m.resolveTenant(ctx, nil)
	m.mu.RLock()
	list := make([]*requestRecord, 0, len(m.requests))
	for _, rec := range m.requests {
		if tenant.Enabled() && rec.TenantID != tenantID {
			continue
		}
		if filter != "" && !strings.EqualFold(rec.Status, filter) {
			continue
		}
		list = append(list, rec)
	}
	m.mu.RUnlock()
	list = uniqueRequestList(list)
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})

	out := &requestmedia.ListRequestsResponse{}
	for _, rec := range list {
		info := &requestmedia.RequestInfo{
			RequestId: rec.ID, ItemType: rec.ItemType, ItemId: rec.ItemID,
			TmdbId: rec.TMDBID, Title: rec.Title, Year: rec.Year, Status: rec.Status,
			CreatedAt: rec.CreatedAt.Format(time.RFC3339), UpdatedAt: rec.UpdatedAt.Format(time.RFC3339),
			RequestedBy: rec.RequestedBy, ApprovedBy: rec.ApprovedBy, Poster: rec.Poster,
		}
		if !rec.ApprovedAt.IsZero() {
			info.ApprovedAt = rec.ApprovedAt.Format(time.RFC3339)
		}
		out.Requests = append(out.Requests, info)
	}
	return out, nil
}

func (m *Module) ApproveRequest(ctx context.Context, req *requestmedia.ApproveRequestRequest) (*requestmedia.ApproveRequestResponse, error) {
	id := strings.TrimSpace(req.GetRequestId())
	if id == "" {
		return &requestmedia.ApproveRequestResponse{Error: "request_id is required"}, nil
	}
	m.mu.RLock()
	rec, ok := m.requests[id]
	m.mu.RUnlock()
	if !ok {
		return &requestmedia.ApproveRequestResponse{RequestId: id, Error: "request not found"}, nil
	}
	if tenant.Enabled() {
		want := m.resolveTenant(ctx, nil)
		if rec.TenantID != "" && rec.TenantID != want {
			return &requestmedia.ApproveRequestResponse{RequestId: id, Error: "request not found"}, nil
		}
	}
	if rec.Status != "pending" {
		return &requestmedia.ApproveRequestResponse{
			RequestId: id, Status: rec.Status, ItemId: rec.ItemID,
			Error: fmt.Sprintf("request is %s, not pending", rec.Status),
		}, nil
	}

	approvedBy := strings.TrimSpace(req.GetApprovedBy())
	if approvedBy == "" {
		approvedBy = "admin"
	}
	itemID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: rec.ID, ItemType: rec.ItemType, TMDBID: rec.TMDBID,
		Title: rec.Title, Year: rec.Year, Overview: rec.Overview, Poster: rec.Poster,
		RequestedBy: rec.RequestedBy, ApprovedBy: approvedBy, TenantID: rec.TenantID,
		PreserveCreate: rec.CreatedAt,
	})
	if err != nil {
		return &requestmedia.ApproveRequestResponse{RequestId: id, Error: err.Error()}, nil
	}
	// Preserve poster from pending row.
	m.mu.Lock()
	if updated, ok := m.requests[id]; ok {
		updated.Poster = rec.Poster
		if m.store != nil {
			_ = m.store.Put(toStoreRecord(updated))
		}
	}
	m.mu.Unlock()
	return &requestmedia.ApproveRequestResponse{RequestId: id, Status: status, ItemId: itemID}, nil
}

func (m *Module) DenyRequest(ctx context.Context, req *requestmedia.DenyRequestRequest) (*requestmedia.DenyRequestResponse, error) {
	id := strings.TrimSpace(req.GetRequestId())
	if id == "" {
		return &requestmedia.DenyRequestResponse{Error: "request_id is required"}, nil
	}
	m.mu.RLock()
	rec, ok := m.requests[id]
	m.mu.RUnlock()
	if !ok {
		return &requestmedia.DenyRequestResponse{RequestId: id, Error: "request not found"}, nil
	}
	if tenant.Enabled() {
		want := m.resolveTenant(ctx, nil)
		if rec.TenantID != "" && rec.TenantID != want {
			return &requestmedia.DenyRequestResponse{RequestId: id, Error: "request not found"}, nil
		}
	}
	if rec.Status != "pending" {
		return &requestmedia.DenyRequestResponse{
			RequestId: id, Status: rec.Status,
			Error: fmt.Sprintf("request is %s, not pending", rec.Status),
		}, nil
	}

	deniedBy := strings.TrimSpace(req.GetDeniedBy())
	if deniedBy == "" {
		deniedBy = "admin"
	}
	now := time.Now().UTC()
	rec.Status = "denied"
	rec.ApprovedBy = deniedBy
	rec.ApprovedAt = now
	rec.UpdatedAt = now
	m.saveRequest(rec)
	return &requestmedia.DenyRequestResponse{RequestId: id, Status: "denied"}, nil
}
