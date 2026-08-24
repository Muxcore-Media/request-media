package internal

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	musicv1 "github.com/Muxcore-Media/media-music/proto/gen/muxcore/music/v1"
)

func (m *Module) musicLibraryClient(ctx context.Context) (musicv1.MusicManagementServiceClient, func(), error) {
	addr, err := m.findModuleAddrPrefer(ctx, "media.library.music", "media.library")
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial media-music: %w", err)
	}
	return musicv1.NewMusicManagementServiceClient(conn), func() { _ = conn.Close() }, nil
}

func (m *Module) ensureMusicArtist(ctx context.Context, client musicv1.MusicManagementServiceClient, artistMBID, artistName string) (string, error) {
	artistMBID = strings.TrimSpace(artistMBID)
	artistName = strings.TrimSpace(artistName)
	if artistMBID != "" {
		resp, err := client.ListArtists(ctx, &musicv1.ListArtistsRequest{Query: artistMBID, Page: 1, PageSize: 50})
		if err == nil {
			for _, a := range resp.GetArtists() {
				if strings.EqualFold(strings.TrimSpace(a.GetMusicbrainzId()), artistMBID) {
					return a.GetId(), nil
				}
			}
		}
	}
	if artistName == "" && artistMBID == "" {
		return "", fmt.Errorf("artist name or musicbrainz id required")
	}
	addResp, err := client.AddArtist(ctx, &musicv1.AddArtistRequest{
		Name: artistName, MusicbrainzId: artistMBID, Monitored: true,
	})
	if err != nil {
		return "", fmt.Errorf("add artist: %w", err)
	}
	return addResp.GetArtist().GetId(), nil
}

func (m *Module) ensureMusicAlbum(ctx context.Context, client musicv1.MusicManagementServiceClient, artistID, rgMBID, title string, year int32) (string, error) {
	rgMBID = strings.TrimSpace(rgMBID)
	title = strings.TrimSpace(title)
	if artistID == "" {
		return "", fmt.Errorf("artist id required")
	}
	if rgMBID != "" {
		alResp, err := client.ListAlbums(ctx, &musicv1.ListAlbumsRequest{ArtistId: artistID})
		if err == nil {
			for _, al := range alResp.GetAlbums() {
				if strings.EqualFold(strings.TrimSpace(al.GetMusicbrainzId()), rgMBID) {
					if !al.GetMonitored() {
						_, _ = client.UpdateAlbumMonitored(ctx, &musicv1.UpdateAlbumMonitoredRequest{
							AlbumId: al.GetId(), Monitored: true,
						})
					}
					return al.GetId(), nil
				}
			}
		}
	}
	addResp, err := client.AddAlbum(ctx, &musicv1.AddAlbumRequest{
		ArtistId: artistID, Title: title, MusicbrainzId: rgMBID, Year: year, Monitored: true,
	})
	if err != nil {
		return "", fmt.Errorf("add album: %w", err)
	}
	return addResp.GetAlbum().GetId(), nil
}

func (m *Module) refreshMusicArtistMetadata(ctx context.Context, client musicv1.MusicManagementServiceClient, artistID string) {
	if artistID == "" {
		return
	}
	_, err := client.RefreshMetadata(ctx, &musicv1.RefreshMetadataRequest{ArtistId: artistID})
	if err != nil {
		// best-effort for full-artist requests
	}
}

func (m *Module) queueMusicAlbum(ctx context.Context, albumID, title string) {
	if albumID == "" {
		return
	}
	m.tryQueueForAcquisition(ctx, queueParams{ItemType: "music", ItemID: albumID, Title: title})
}
