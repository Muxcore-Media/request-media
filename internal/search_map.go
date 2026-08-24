package internal

import (
	"sort"
	"strings"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
)

func searchTypeFromQuery(raw string) metadatav1.MediaType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "movie", "movies":
		return metadatav1.MediaType_MEDIA_TYPE_MOVIE
	case "tv", "show", "shows", "series":
		return metadatav1.MediaType_MEDIA_TYPE_TV
	default:
		return metadatav1.MediaType_MEDIA_TYPE_UNSPECIFIED
	}
}

func isMusicRequest(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "music", "artist", "music_artist":
		return true
	default:
		return false
	}
}

func isMusicAlbumSearch(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "music_album", "album":
		return true
	default:
		return false
	}
}

func isMusicAlbumRequest(raw string) bool {
	return isMusicAlbumSearch(raw)
}

func isMusicTrackSearch(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "music_track", "track", "recording", "song":
		return true
	default:
		return false
	}
}

func isMusicTrackRequest(raw string) bool {
	return isMusicTrackSearch(raw)
}

func isTVRequest(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "tv", "show", "shows", "series":
		return true
	default:
		return false
	}
}

func mapSearchResults(query string, reqType metadatav1.MediaType, results []*metadatav1.SearchResult) []searchResult {
	out := make([]searchResult, 0, len(results))
	for _, r := range results {
		mapped, ok := mapSearchResult(reqType, r)
		if ok {
			out = append(out, mapped)
		}
	}
	rankSearchResults(query, out)
	return out
}

func mapSearchResult(reqType metadatav1.MediaType, r *metadatav1.SearchResult) (searchResult, bool) {
	if r == nil {
		return searchResult{}, false
	}
	mt := r.GetMediaType()
	switch reqType {
	case metadatav1.MediaType_MEDIA_TYPE_MOVIE:
		if mt == metadatav1.MediaType_MEDIA_TYPE_TV {
			return searchResult{}, false
		}
		mt = metadatav1.MediaType_MEDIA_TYPE_MOVIE
	case metadatav1.MediaType_MEDIA_TYPE_TV:
		if mt == metadatav1.MediaType_MEDIA_TYPE_MOVIE {
			return searchResult{}, false
		}
		mt = metadatav1.MediaType_MEDIA_TYPE_TV
	default:
		if mt != metadatav1.MediaType_MEDIA_TYPE_MOVIE && mt != metadatav1.MediaType_MEDIA_TYPE_TV {
			return searchResult{}, false
		}
	}

	title := strings.TrimSpace(r.GetTitle())
	if title == "" {
		title = strings.TrimSpace(r.GetName())
	}
	if title == "" {
		return searchResult{}, false
	}
	year := extractYear(r.GetReleaseDate())
	if year == 0 {
		year = extractYear(r.GetFirstAirDate())
	}
	kind := "movie"
	if mt == metadatav1.MediaType_MEDIA_TYPE_TV {
		kind = "tv"
	}
	return searchResult{
		ID:        r.GetId(),
		Title:     title,
		Year:      year,
		Overview:  r.GetOverview(),
		Poster:    r.GetPosterPath(),
		VoteAvg:   r.GetVoteAverage(),
		MediaType: kind,
	}, true
}

func rankSearchResults(query string, results []searchResult) {
	q := strings.ToLower(strings.TrimSpace(query))
	sort.SliceStable(results, func(i, j int) bool {
		return searchRank(results[i], q) > searchRank(results[j], q)
	})
}

func searchRank(r searchResult, q string) int {
	t := strings.ToLower(strings.TrimSpace(r.Title))
	n := 0
	if t == q {
		n += 100
	} else if q != "" && strings.HasPrefix(t, q) {
		n += 40
	}
	if r.MediaType == "tv" {
		n += 15
	}
	return n
}
