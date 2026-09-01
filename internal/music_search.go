package internal

import (
	"context"
	"net/http"
	"sort"
	"strings"

	musicbrainzv1 "github.com/Muxcore-Media/metadata-musicbrainz/proto/musicbrainzv1"
)

func (m *Module) musicBrainzClient(ctx context.Context) (musicbrainzv1.MusicBrainzServiceClient, func(), error) {
	addr, err := m.findModuleAddrPrefer(ctx, "metadata.music", "metadata.musicbrainz")
	if err != nil {
		return nil, nil, err
	}
	conn, err := dialModuleGRPC(addr)
	if err != nil {
		return nil, nil, err
	}
	return musicbrainzv1.NewMusicBrainzServiceClient(conn), func() { _ = conn.Close() }, nil
}

func (m *Module) handleMusicSearch(w http.ResponseWriter, r *http.Request, q string) {
	cli, closeFn, err := m.musicBrainzClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "music metadata module unavailable", "results": []searchResult{},
		})
		return
	}
	defer closeFn()

	ctx, cancel := handlerContext(r)
	defer cancel()

	resp, err := cli.SearchArtist(ctx, &musicbrainzv1.SearchArtistRequest{Query: q, Limit: 10})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(), "results": []searchResult{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": m.overlaySearchResults(r.Context(), m.resolveTenant(r.Context(), r.Header),
			mapMusicArtistSearchResults(q, resp.GetArtists())),
	})
}

func (m *Module) handleMusicAlbumSearch(w http.ResponseWriter, r *http.Request, q string) {
	cli, closeFn, err := m.musicBrainzClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "music metadata module unavailable", "results": []searchResult{},
		})
		return
	}
	defer closeFn()

	ctx, cancel := handlerContext(r)
	defer cancel()

	resp, err := cli.SearchReleaseGroup(ctx, &musicbrainzv1.SearchReleaseGroupRequest{Query: q, Limit: 10})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(), "results": []searchResult{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": m.overlaySearchResults(r.Context(), m.resolveTenant(r.Context(), r.Header),
			mapMusicAlbumSearchResults(q, resp.GetReleaseGroups())),
	})
}

func (m *Module) handleMusicTrackSearch(w http.ResponseWriter, r *http.Request, q string) {
	cli, closeFn, err := m.musicBrainzClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "music metadata module unavailable", "results": []searchResult{},
		})
		return
	}
	defer closeFn()

	ctx, cancel := handlerContext(r)
	defer cancel()

	resp, err := cli.SearchRecording(ctx, &musicbrainzv1.SearchRecordingRequest{Query: q, Limit: 10})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(), "results": []searchResult{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": m.overlaySearchResults(r.Context(), m.resolveTenant(r.Context(), r.Header),
			mapMusicTrackSearchResults(q, resp.GetRecordings())),
	})
}

func mapMusicArtistSearchResults(query string, artists []*musicbrainzv1.ArtistSearchResult) []searchResult {
	out := make([]searchResult, 0, len(artists))
	for _, a := range artists {
		if a == nil || strings.TrimSpace(a.GetName()) == "" {
			continue
		}
		out = append(out, searchResult{
			MusicBrainzID: a.GetId(),
			Title:         a.GetName(),
			Overview:      a.GetDisambiguation(),
			MediaType:     "music",
		})
	}
	rankMusicSearchResults(query, out)
	return out
}

func mapMusicAlbumSearchResults(query string, groups []*musicbrainzv1.ReleaseGroupSearchResult) []searchResult {
	out := make([]searchResult, 0, len(groups))
	for _, rg := range groups {
		if rg == nil || strings.TrimSpace(rg.GetTitle()) == "" {
			continue
		}
		title := rg.GetTitle()
		if rg.GetArtistName() != "" {
			title = rg.GetArtistName() + " — " + rg.GetTitle()
		}
		out = append(out, searchResult{
			ReleaseGroupID: rg.GetId(),
			MusicBrainzID:  rg.GetArtistId(),
			ArtistName:     rg.GetArtistName(),
			AlbumTitle:     rg.GetTitle(),
			Title:          title,
			Year:           rg.GetFirstReleaseDateYear(),
			MediaType:      "music_album",
		})
	}
	rankMusicSearchResults(query, out)
	return out
}

func mapMusicTrackSearchResults(query string, recs []*musicbrainzv1.RecordingSearchResult) []searchResult {
	out := make([]searchResult, 0, len(recs))
	for _, rec := range recs {
		if rec == nil || strings.TrimSpace(rec.GetTitle()) == "" {
			continue
		}
		title := rec.GetTitle()
		if rec.GetArtistName() != "" {
			title = rec.GetArtistName() + " — " + rec.GetTitle()
		}
		out = append(out, searchResult{
			RecordingID:    rec.GetId(),
			ReleaseGroupID: rec.GetReleaseGroupId(),
			MusicBrainzID:  rec.GetArtistId(),
			ArtistName:     rec.GetArtistName(),
			AlbumTitle:     rec.GetReleaseGroupTitle(),
			Title:          title,
			MediaType:      "music_track",
		})
	}
	rankMusicSearchResults(query, out)
	return out
}

func rankMusicSearchResults(query string, out []searchResult) {
	q := strings.ToLower(strings.TrimSpace(query))
	sort.SliceStable(out, func(i, j int) bool {
		return musicSearchRank(out[i], q) > musicSearchRank(out[j], q)
	})
}

func musicSearchRank(r searchResult, q string) int {
	t := strings.ToLower(strings.TrimSpace(r.Title))
	n := 0
	if t == q {
		n += 100
	} else if q != "" && strings.HasPrefix(t, q) {
		n += 40
	}
	return n
}
