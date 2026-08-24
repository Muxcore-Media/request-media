package internal

import (
	"context"
	"fmt"
	"log/slog"
)

const eventMusicRequested = "media.music.requested"

func (m *Module) fulfillMusic(ctx context.Context, p fulfillParams) (string, string, error) {
	recBase := musicRecBase(p)

	itemKey := p.MusicBrainzID
	if itemKey == "" {
		itemKey = p.Title
	}

	if m.getPreferWorkflow() {
		if runID, ok := m.tryRunWorkflow(ctx, "music-request", map[string]string{
			"title": p.Title, "musicbrainz_id": p.MusicBrainzID,
			"request_id": p.RequestID,
		}); ok {
			m.saveRequest(recBase("workflow", ""))
			go m.publish(context.Background(), eventMusicRequested, map[string]interface{}{
				"request_id": p.RequestID, "musicbrainz_id": p.MusicBrainzID,
				"title": p.Title, "run_id": runID, "kind": "artist",
			})
			m.tryQueueForAcquisition(ctx, queueParams{
				ItemType: "music", ItemID: fmt.Sprintf("mbid_%s", itemKey),
				Title: p.Title,
			})
			return "", "workflow", nil
		}
	}

	client, closeFn, err := m.musicLibraryClient(ctx)
	if err != nil {
		m.saveRequest(recBase("requested", ""))
		go m.publish(context.Background(), eventMusicRequested, map[string]interface{}{
			"request_id": p.RequestID, "musicbrainz_id": p.MusicBrainzID, "title": p.Title, "kind": "artist",
		})
		m.tryQueueForAcquisition(ctx, queueParams{
			ItemType: "music", ItemID: fmt.Sprintf("mbid_%s", itemKey), Title: p.Title,
		})
		slog.Info("music requested (library unavailable)", "title", p.Title, "mbid", p.MusicBrainzID)
		return "", "requested", nil
	}
	defer closeFn()

	artistID, err := m.ensureMusicArtist(ctx, client, p.MusicBrainzID, p.Title)
	if err != nil {
		return "", "", err
	}
	m.refreshMusicArtistMetadata(ctx, client, artistID)

	m.saveRequest(recBase("added", artistID))
	go m.publish(context.Background(), eventMusicRequested, map[string]interface{}{
		"request_id": p.RequestID, "artist_id": artistID,
		"musicbrainz_id": p.MusicBrainzID, "title": p.Title, "kind": "artist",
	})
	m.tryQueueForAcquisition(ctx, queueParams{
		ItemType: "music", ItemID: artistID, Title: p.Title,
	})
	slog.Info("music requested", "title", p.Title, "mbid", p.MusicBrainzID, "artist_id", artistID)
	return artistID, "added", nil
}
