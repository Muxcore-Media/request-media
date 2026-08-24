package internal

import (
	"net/http"
	"strconv"
	"strings"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
)

func (m *Module) handleDiscoverBrowse(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) != 3 {
		return false
	}
	category := strings.ToLower(parts[0])
	mediaType, ok := discoverTrendingMediaType(parts[1])
	if !ok {
		http.NotFound(w, r)
		return true
	}

	client, conn, err := m.metadataClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "metadata module unavailable", "results": []searchResult{}})
		return true
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := handlerContext(r)
	defer cancel()

	switch category {
	case "trending":
		window := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("window")))
		if window == "" {
			window = "week"
		}
		tw := metadatav1.TrendingTimeWindow_TRENDING_TIME_WINDOW_WEEK
		if window == "day" {
			tw = metadatav1.TrendingTimeWindow_TRENDING_TIME_WINDOW_DAY
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		resp, err := client.ListTrending(ctx, &metadatav1.ListTrendingRequest{
			MediaType: mediaType, TimeWindow: tw, Page: int32(page),
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": []searchResult{}})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": mapSearchResults("", browseMediaType(parts[1]), resp.GetResults())})
	case "popular":
		popType := metadatav1.MediaType_MEDIA_TYPE_MOVIE
		if mediaType == metadatav1.TrendingMediaType_TRENDING_MEDIA_TYPE_TV {
			popType = metadatav1.MediaType_MEDIA_TYPE_TV
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		resp, err := client.ListPopular(ctx, &metadatav1.ListPopularRequest{
			Type: popType, Page: int32(page),
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "results": []searchResult{}})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": mapSearchResults("", popType, resp.GetResults())})
	default:
		return false
	}
	return true
}

func discoverTrendingMediaType(kind string) (metadatav1.TrendingMediaType, bool) {
	switch strings.ToLower(kind) {
	case "movie", "movies":
		return metadatav1.TrendingMediaType_TRENDING_MEDIA_TYPE_MOVIE, true
	case "tv", "series", "show", "shows":
		return metadatav1.TrendingMediaType_TRENDING_MEDIA_TYPE_TV, true
	default:
		return metadatav1.TrendingMediaType_TRENDING_MEDIA_TYPE_UNSPECIFIED, false
	}
}

func browseMediaType(kind string) metadatav1.MediaType {
	if mt, ok := discoverTrendingMediaType(kind); ok && mt == metadatav1.TrendingMediaType_TRENDING_MEDIA_TYPE_TV {
		return metadatav1.MediaType_MEDIA_TYPE_TV
	}
	return metadatav1.MediaType_MEDIA_TYPE_MOVIE
}
