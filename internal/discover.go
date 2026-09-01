package internal

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
)

type discoverTrailer struct {
	Name       string `json:"name"`
	YoutubeKey string `json:"youtubeKey"`
	URL        string `json:"url"`
}

type discoverDetail struct {
	ID        int32            `json:"id"`
	Title     string           `json:"title"`
	Year      int32            `json:"year"`
	Overview  string           `json:"overview"`
	Tagline   string           `json:"tagline"`
	Genres    []string         `json:"genres"`
	Poster    string           `json:"poster"`
	Backdrop  string           `json:"backdrop"`
	VoteAvg   float64          `json:"voteAvg"`
	Runtime   int32            `json:"runtime,omitempty"`
	Status        string           `json:"status,omitempty"`
	MediaType     string           `json:"mediaType"`
	RequestStatus string           `json:"requestStatus,omitempty"`
	Trailer       *discoverTrailer `json:"trailer,omitempty"`
}

func (m *Module) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/discover/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 {
		switch strings.ToLower(parts[0]) {
		case "trending", "popular":
			if m.handleDiscoverBrowse(w, r, parts) {
				return
			}
		}
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	kind := strings.ToLower(parts[0])
	id, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	client, conn, err := m.metadataClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "metadata module unavailable"})
		return
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := handlerContext(r)
	defer cancel()

	tenantID := m.resolveTenant(r.Context(), r.Header)
	switch kind {
	case "movie", "movies":
		resp, err := client.GetMovieDetails(ctx, &metadatav1.GetMovieDetailsRequest{
			Id:               int32(id),
			AppendToResponse: []string{"videos"},
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, m.overlayDiscoverDetail(r.Context(), tenantID, mapMovieDiscover(resp)))
	case "tv", "series", "show", "shows":
		resp, err := client.GetTVDetails(ctx, &metadatav1.GetTVDetailsRequest{
			Id:               int32(id),
			AppendToResponse: []string{"videos"},
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, m.overlayDiscoverDetail(r.Context(), tenantID, mapTVDiscover(resp)))
	default:
		http.NotFound(w, r)
	}
}

func mapMovieDiscover(resp *metadatav1.GetMovieDetailsResponse) discoverDetail {
	title := strings.TrimSpace(resp.GetTitle())
	poster := resp.GetPosterPath()
	if poster == "" {
		poster = resp.GetPosterUrl()
	}
	backdrop := resp.GetBackdropPath()
	if backdrop == "" {
		backdrop = resp.GetBackdropUrl()
	}
	return discoverDetail{
		ID:        resp.GetId(),
		Title:     title,
		Year:      extractYear(resp.GetReleaseDate()),
		Overview:  resp.GetOverview(),
		Tagline:   resp.GetTagline(),
		Genres:    genreNames(resp.GetGenres()),
		Poster:    poster,
		Backdrop:  backdrop,
		VoteAvg:   resp.GetVoteAverage(),
		Runtime:   resp.GetRuntime(),
		Status:    resp.GetStatus(),
		MediaType: "movie",
		Trailer:   pickTrailer(resp.GetVideos()),
	}
}

func mapTVDiscover(resp *metadatav1.GetTVDetailsResponse) discoverDetail {
	title := strings.TrimSpace(resp.GetName())
	poster := resp.GetPosterPath()
	if poster == "" {
		poster = resp.GetPosterUrl()
	}
	backdrop := resp.GetBackdropPath()
	if backdrop == "" {
		backdrop = resp.GetBackdropUrl()
	}
	return discoverDetail{
		ID:        resp.GetId(),
		Title:     title,
		Year:      extractYear(resp.GetFirstAirDate()),
		Overview:  resp.GetOverview(),
		Tagline:   resp.GetTagline(),
		Genres:    genreNames(resp.GetGenres()),
		Poster:    poster,
		Backdrop:  backdrop,
		VoteAvg:   resp.GetVoteAverage(),
		Status:    resp.GetStatus(),
		MediaType: "tv",
		Trailer:   pickTrailer(resp.GetVideos()),
	}
}

func genreNames(genres []*metadatav1.Genre) []string {
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		if name := strings.TrimSpace(g.GetName()); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func pickTrailer(videos []*metadatav1.Video) *discoverTrailer {
	type rank struct {
		score int
		v     *metadatav1.Video
	}
	best := rank{score: -1}
	for _, v := range videos {
		if v == nil || strings.ToLower(strings.TrimSpace(v.GetSite())) != "youtube" {
			continue
		}
		key := strings.TrimSpace(v.GetKey())
		if key == "" {
			continue
		}
		score := 0
		switch strings.ToLower(strings.TrimSpace(v.GetType())) {
		case "trailer":
			score = 100
		case "teaser":
			score = 80
		case "clip":
			score = 40
		default:
			score = 20
		}
		if score > best.score {
			best = rank{score: score, v: v}
		}
	}
	if best.v == nil {
		return nil
	}
	key := strings.TrimSpace(best.v.GetKey())
	return &discoverTrailer{
		Name:       strings.TrimSpace(best.v.GetName()),
		YoutubeKey: key,
		URL:        "https://www.youtube.com/watch?v=" + key,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("request-media: write JSON response", "error", err)
	}
}
