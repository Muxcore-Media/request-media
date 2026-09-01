package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"google.golang.org/grpc"

	musicbrainzv1 "github.com/Muxcore-Media/metadata-musicbrainz/proto/musicbrainzv1"
)

type stubMusicBrainz struct {
	musicbrainzv1.UnimplementedMusicBrainzServiceServer
}

func (stubMusicBrainz) SearchArtist(_ context.Context, req *musicbrainzv1.SearchArtistRequest) (*musicbrainzv1.SearchArtistResponse, error) {
	return &musicbrainzv1.SearchArtistResponse{
		Artists: []*musicbrainzv1.ArtistSearchResult{{
			Id: "mbid-artist-1", Name: "Fixture Artist", Disambiguation: "test",
		}},
	}, nil
}

func (stubMusicBrainz) SearchReleaseGroup(_ context.Context, req *musicbrainzv1.SearchReleaseGroupRequest) (*musicbrainzv1.SearchReleaseGroupResponse, error) {
	return &musicbrainzv1.SearchReleaseGroupResponse{
		ReleaseGroups: []*musicbrainzv1.ReleaseGroupSearchResult{{
			Id: "mbid-rg-1", Title: "Fixture Album", ArtistId: "mbid-artist-1", ArtistName: "Fixture Artist",
			FirstReleaseDateYear: 2020,
		}},
	}, nil
}

func TestHTTPMusicSearch_ArtistAndAlbum(t *testing.T) {
	m, base, client := testModuleHTTP(t)
	addr := startGRPC(t, func(s *grpc.Server) {
		musicbrainzv1.RegisterMusicBrainzServiceServer(s, stubMusicBrainz{})
	})
	m.findAddr = func(_ context.Context, capability string) (string, error) {
		switch capability {
		case "metadata.music", "metadata.musicbrainz":
			return addr, nil
		default:
			return "", nil
		}
	}

	for _, spec := range []struct {
		path, wantType string
	}{
		{"/api/search?q=fixture&type=music", "music"},
		{"/api/search?q=fixture&type=music_album", "music_album"},
	} {
		resp, err := client.Get(base + spec.path)
		if err != nil {
			t.Fatalf("%s: %v", spec.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", spec.path, resp.StatusCode, body)
		}
		var out struct {
			Results []searchResult `json:"results"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 || out.Results[0].MediaType != spec.wantType {
			t.Fatalf("%s results=%+v", spec.path, out.Results)
		}
	}
}
