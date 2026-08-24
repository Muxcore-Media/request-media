package internal

import "testing"

func TestIsMusicAlbumRequest(t *testing.T) {
	if !isMusicAlbumRequest("music_album") {
		t.Fatal("expected music_album")
	}
	if isMusicAlbumRequest("music") {
		t.Fatal("music should not match album")
	}
}

func TestIsMusicTrackRequest(t *testing.T) {
	if !isMusicTrackRequest("music_track") {
		t.Fatal("expected music_track")
	}
	if !isMusicTrackRequest("song") {
		t.Fatal("expected song alias")
	}
}

func TestIsMusicRequestArtistOnly(t *testing.T) {
	if !isMusicRequest("music") {
		t.Fatal("expected music")
	}
	if isMusicRequest("album") {
		t.Fatal("album search should not route to artist request")
	}
}
