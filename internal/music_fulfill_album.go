package internal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	musicbrainzv1 "github.com/Muxcore-Media/metadata-musicbrainz/proto/musicbrainzv1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func (m *Module) RequestAlbum(ctx context.Context, req *requestmedia.RequestAlbumRequest) (*requestmedia.RequestAlbumResponse, error) {
	if err := m.requireRequestRoles(ctx); err != nil {
		return nil, err
	}
	ctx = incomingContextWithUser(ctx, req.GetRequestedBy())
	requestedBy := requestedByFromContext(ctx, req.GetRequestedBy())
	roles := rolesFromContext(ctx)
	tenantID := m.resolveTenant(ctx, nil)
	rgID := strings.TrimSpace(req.GetReleaseGroupId())
	if existing := m.findExistingMusicAlbum(rgID, req.GetTitle(), tenantID); existing != nil {
		return &requestmedia.RequestAlbumResponse{
			RequestId: existing.ID, AlbumId: existing.ItemID, Status: existing.Status,
		}, nil
	}
	requestID := fmt.Sprintf("req_malb_%d", time.Now().UnixNano())

	if m.needsApproval(requestedBy, roles) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "music_album", Title: req.GetTitle(), Year: req.GetYear(),
			Overview: req.GetOverview(), Status: "pending", RequestedBy: requestedBy, TenantID: tenantID,
			ReleaseGroupID: rgID, MusicBrainzID: strings.TrimSpace(req.GetArtistMusicbrainzId()),
			ArtistName: strings.TrimSpace(req.GetArtistName()),
		})
		return &requestmedia.RequestAlbumResponse{RequestId: requestID, Status: "pending"}, nil
	}

	albumID, artistID, status, err := m.fulfillMusicAlbum(ctx, fulfillParams{
		RequestID: requestID, ItemType: "music_album", ReleaseGroupID: rgID,
		MusicBrainzID: strings.TrimSpace(req.GetArtistMusicbrainzId()),
		ArtistName:    strings.TrimSpace(req.GetArtistName()), Title: req.GetTitle(), Year: req.GetYear(),
		Overview: req.GetOverview(), RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestAlbumResponse{
		RequestId: requestID, AlbumId: albumID, ArtistId: artistID, Status: status,
	}, nil
}

func (m *Module) RequestTrack(ctx context.Context, req *requestmedia.RequestTrackRequest) (*requestmedia.RequestTrackResponse, error) {
	if err := m.requireRequestRoles(ctx); err != nil {
		return nil, err
	}
	ctx = incomingContextWithUser(ctx, req.GetRequestedBy())
	requestedBy := requestedByFromContext(ctx, req.GetRequestedBy())
	roles := rolesFromContext(ctx)
	tenantID := m.resolveTenant(ctx, nil)
	recID := strings.TrimSpace(req.GetRecordingId())
	if existing := m.findExistingMusicTrack(recID, req.GetTitle(), tenantID); existing != nil {
		return &requestmedia.RequestTrackResponse{
			RequestId: existing.ID, AlbumId: existing.ItemID, Status: existing.Status,
		}, nil
	}
	requestID := fmt.Sprintf("req_mtrk_%d", time.Now().UnixNano())

	if m.needsApproval(requestedBy, roles) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "music_track", Title: req.GetTitle(), Status: "pending",
			RequestedBy: requestedBy, TenantID: tenantID, RecordingID: recID,
			ReleaseGroupID: strings.TrimSpace(req.GetReleaseGroupId()),
			MusicBrainzID:  strings.TrimSpace(req.GetArtistMusicbrainzId()),
			ArtistName:     strings.TrimSpace(req.GetArtistName()), AlbumTitle: strings.TrimSpace(req.GetAlbumTitle()),
		})
		return &requestmedia.RequestTrackResponse{RequestId: requestID, Status: "pending"}, nil
	}

	albumID, artistID, status, err := m.fulfillMusicTrack(ctx, fulfillParams{
		RequestID: requestID, ItemType: "music_track", RecordingID: recID,
		ReleaseGroupID: strings.TrimSpace(req.GetReleaseGroupId()),
		MusicBrainzID:  strings.TrimSpace(req.GetArtistMusicbrainzId()),
		ArtistName:     strings.TrimSpace(req.GetArtistName()), Title: req.GetTitle(),
		AlbumTitle: strings.TrimSpace(req.GetAlbumTitle()), RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestTrackResponse{
		RequestId: requestID, AlbumId: albumID, ArtistId: artistID, Status: status,
	}, nil
}

func (m *Module) fulfillMusicAlbum(ctx context.Context, p fulfillParams) (albumID, artistID, status string, err error) {
	rgID := strings.TrimSpace(p.ReleaseGroupID)
	artistMBID := strings.TrimSpace(p.MusicBrainzID)
	artistName := strings.TrimSpace(p.ArtistName)
	title := strings.TrimSpace(p.Title)
	year := p.Year

	if rgID != "" && (artistMBID == "" || title == "") {
		if resolved, err := m.resolveReleaseGroup(ctx, rgID); err == nil {
			if artistMBID == "" {
				artistMBID = resolved.ArtistID
			}
			if artistName == "" {
				artistName = resolved.ArtistName
			}
			if title == "" {
				title = resolved.Title
			}
			if year == 0 {
				year = resolved.Year
			}
		}
	}
	if title == "" {
		return "", "", "", fmt.Errorf("album title required")
	}

	recBase := musicRecBase(p)
	client, closeFn, err := m.musicLibraryClient(ctx)
	if err != nil {
		m.saveRequest(recBase("requested", rgID))
		m.queueMusicAlbum(ctx, rgID, title)
		slog.Info("music album requested (library unavailable)", "title", title, "rg", rgID)
		return rgID, "", "requested", nil
	}
	defer closeFn()

	artistID, err = m.ensureMusicArtist(ctx, client, artistMBID, artistName)
	if err != nil {
		return "", "", "", err
	}
	albumID, err = m.ensureMusicAlbum(ctx, client, artistID, rgID, title, year)
	if err != nil {
		return "", "", "", err
	}
	m.saveRequest(recBase("added", albumID))
	go m.publish(context.Background(), eventMusicRequested, map[string]interface{}{
		"request_id": p.RequestID, "artist_id": artistID, "album_id": albumID,
		"release_group_id": rgID, "title": title, "kind": "album",
	})
	m.queueMusicAlbum(ctx, albumID, artistName+" - "+title)
	slog.Info("music album requested", "title", title, "album_id", albumID, "artist_id", artistID)
	return albumID, artistID, "added", nil
}

func (m *Module) fulfillMusicTrack(ctx context.Context, p fulfillParams) (albumID, artistID, status string, err error) {
	recID := strings.TrimSpace(p.RecordingID)
	rgID := strings.TrimSpace(p.ReleaseGroupID)
	artistMBID := strings.TrimSpace(p.MusicBrainzID)
	artistName := strings.TrimSpace(p.ArtistName)
	title := strings.TrimSpace(p.Title)
	albumTitle := strings.TrimSpace(p.AlbumTitle)

	if recID != "" {
		if resolved, err := m.resolveRecording(ctx, recID); err == nil {
			if rgID == "" {
				rgID = resolved.ReleaseGroupID
			}
			if artistMBID == "" {
				artistMBID = resolved.ArtistID
			}
			if artistName == "" {
				artistName = resolved.ArtistName
			}
			if title == "" {
				title = resolved.Title
			}
			if albumTitle == "" {
				albumTitle = resolved.ReleaseGroupTitle
			}
		}
	}
	if rgID == "" {
		return "", "", "", fmt.Errorf("release group id required for track request")
	}
	if title == "" {
		return "", "", "", fmt.Errorf("track title required")
	}
	if albumTitle == "" {
		albumTitle = title
	}

	p2 := p
	p2.ItemType = "music_album"
	p2.ReleaseGroupID = rgID
	p2.MusicBrainzID = artistMBID
	p2.ArtistName = artistName
	p2.Title = albumTitle
	p2.Year = 0
	return m.fulfillMusicAlbum(ctx, p2)
}

type resolvedReleaseGroup struct {
	ID, Title, ArtistID, ArtistName string
	Year                            int32
}

type resolvedRecording struct {
	ID, Title, ArtistID, ArtistName, ReleaseGroupID, ReleaseGroupTitle string
}

func (m *Module) resolveReleaseGroup(ctx context.Context, rgID string) (*resolvedReleaseGroup, error) {
	cli, closeFn, err := m.musicBrainzClient(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	resp, err := cli.GetReleaseGroup(ctx, &musicbrainzv1.GetReleaseGroupRequest{MusicbrainzId: rgID})
	if err != nil {
		return nil, err
	}
	return &resolvedReleaseGroup{
		ID: resp.GetId(), Title: resp.GetTitle(), ArtistID: resp.GetArtistId(),
		ArtistName: resp.GetArtistName(), Year: resp.GetFirstReleaseDateYear(),
	}, nil
}

func (m *Module) resolveRecording(ctx context.Context, recID string) (*resolvedRecording, error) {
	cli, closeFn, err := m.musicBrainzClient(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	resp, err := cli.GetRecording(ctx, &musicbrainzv1.GetRecordingRequest{MusicbrainzId: recID})
	if err != nil {
		return nil, err
	}
	return &resolvedRecording{
		ID: resp.GetId(), Title: resp.GetTitle(), ArtistID: resp.GetArtistId(), ArtistName: resp.GetArtistName(),
		ReleaseGroupID: resp.GetReleaseGroupId(), ReleaseGroupTitle: resp.GetReleaseGroupTitle(),
	}, nil
}

func (m *Module) findExistingMusicAlbum(rgID, title, tenantID string) *requestRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rec := range m.requests {
		if rec.ItemType != "music_album" {
			continue
		}
		if tenantID != "" && rec.TenantID != "" && rec.TenantID != tenantID {
			continue
		}
		if rgID != "" && rec.ReleaseGroupID == rgID {
			return rec
		}
		if rgID == "" && title != "" && strings.EqualFold(rec.Title, title) {
			return rec
		}
	}
	return nil
}

func (m *Module) findExistingMusicTrack(recID, title, tenantID string) *requestRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rec := range m.requests {
		if rec.ItemType != "music_track" {
			continue
		}
		if tenantID != "" && rec.TenantID != "" && rec.TenantID != tenantID {
			continue
		}
		if recID != "" && rec.RecordingID == recID {
			return rec
		}
		if recID == "" && title != "" && strings.EqualFold(rec.Title, title) {
			return rec
		}
	}
	return nil
}

func musicRecBase(p fulfillParams) func(status, itemID string) *requestRecord {
	return func(status, itemID string) *requestRecord {
		rec := &requestRecord{
			ID: p.RequestID, ItemType: p.ItemType, ItemID: itemID,
			Title: p.Title, Year: p.Year, Overview: p.Overview, Poster: p.Poster, Status: status,
			RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, TenantID: p.TenantID,
			MusicBrainzID: p.MusicBrainzID, ReleaseGroupID: p.ReleaseGroupID,
			RecordingID: p.RecordingID, ArtistName: p.ArtistName, AlbumTitle: p.AlbumTitle,
		}
		if !p.PreserveCreate.IsZero() {
			rec.CreatedAt = p.PreserveCreate
		}
		if p.ApprovedBy != "" {
			rec.ApprovedAt = time.Now().UTC()
		}
		return rec
	}
}
